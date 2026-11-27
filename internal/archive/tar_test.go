package archive

import (
	archivetar "archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type tarFixture struct {
	name     string
	contents []byte
	typeflag byte
	linkname string
	format   archivetar.Format
	mode     int64
	modified time.Time
}

func TestTARAndTARGZDetectionMetadataAndRepeatedStreaming(t *testing.T) {
	t.Parallel()
	modified := time.Date(2026, time.November, 27, 12, 0, 0, 0, time.UTC)
	fixtures := []tarFixture{
		{name: "empty/", typeflag: archivetar.TypeDir, mode: 0o755, modified: modified},
		{name: "nested/файл с пробелами.txt", contents: []byte("данные"), mode: 0o640, modified: modified},
	}
	for _, compressed := range []bool{false, true} {
		compressed := compressed
		t.Run(map[bool]string{false: "tar", true: "tar.gz"}[compressed], func(t *testing.T) {
			t.Parallel()
			session, input := openTARBytes(t, makeTAR(t, fixtures, compressed))
			defer session.Close()
			wantFormat := TARFormat
			if compressed {
				wantFormat = TARGZFormat
			}
			if session.Format() != wantFormat {
				t.Fatalf("format=%q", session.Format())
			}
			if compressed {
				metadata, ok := session.(ArchiveCompressionMetadata)
				if !ok {
					t.Fatal("TAR.GZ session has no compression metadata")
				}
				compressedSize := metadata.TotalCompressedSize()
				if compressedSize == nil || *compressedSize != int64(len(input.data)) {
					t.Fatalf("compression metadata ok=%v size=%v", ok, compressedSize)
				}
			}
			root, err := session.ReadDir(context.Background(), "/")
			if err != nil || !reflect.DeepEqual(entryNames(root), []string{"empty", "nested"}) {
				t.Fatalf("root=%+v error=%v", root, err)
			}
			entry, err := session.Stat(context.Background(), "/nested/файл с пробелами.txt")
			if err != nil || entry.RawPath != "nested/файл с пробелами.txt" || entry.Size == nil ||
				*entry.Size != int64(len([]byte("данные"))) || entry.ModifiedAt == nil ||
				entry.Extra["tar.mode"] != "640" || entry.CompressedSize != nil {
				t.Fatalf("entry=%+v error=%v", entry, err)
			}
			for attempt := 0; attempt < 3; attempt++ {
				stream, err := session.Open(context.Background(), entry.Path)
				if err != nil {
					t.Fatal(err)
				}
				contents, readErr := io.ReadAll(stream)
				closeErr := stream.Close()
				if readErr != nil || closeErr != nil || string(contents) != "данные" {
					t.Fatalf("attempt=%d contents=%q read=%v close=%v", attempt, contents, readErr, closeErr)
				}
			}
			if input.closeCount() != 0 {
				t.Fatal("input closed while session is alive")
			}
		})
	}
}

func TestTARFormatsLongNamesAndLinks(t *testing.T) {
	t.Parallel()
	longName := strings.Repeat("длинный-каталог/", 10) + "файл.txt"
	for _, format := range []archivetar.Format{archivetar.FormatUSTAR, archivetar.FormatGNU, archivetar.FormatPAX} {
		format := format
		t.Run(format.String(), func(t *testing.T) {
			name := "regular.txt"
			if format != archivetar.FormatUSTAR {
				name = longName
			}
			data := makeTAR(t, []tarFixture{
				{name: name, contents: []byte("ok"), format: format},
				{name: "symbolic", typeflag: archivetar.TypeSymlink, linkname: name, format: format},
				{name: "hard", typeflag: archivetar.TypeLink, linkname: name, format: format},
				{name: "fifo", typeflag: archivetar.TypeFifo, format: format},
				{name: "character", typeflag: archivetar.TypeChar, format: format},
				{name: "block", typeflag: archivetar.TypeBlock, format: format},
			}, false)
			session, _ := openTARBytes(t, data)
			defer session.Close()
			for path, kind := range map[string]EntryKind{
				"/symbolic": EntrySymlink, "/hard": EntryHardlink, "/fifo": EntrySpecial,
				"/character": EntrySpecial, "/block": EntrySpecial,
			} {
				entry, err := session.Stat(context.Background(), path)
				if err != nil || entry.Kind != kind {
					t.Fatalf("%s entry=%+v error=%v", path, entry, err)
				}
			}
			if _, err := NewDefaultExtractor().Extract(context.Background(), session, newFakeDestination(), nil); !errors.Is(err, ErrUnsupportedEntry) {
				t.Fatalf("extraction error=%v", err)
			}
		})
	}
}

func TestTARRejectsUnsafeCorruptAndUnboundedEntries(t *testing.T) {
	t.Parallel()
	unsafe := makeTAR(t, []tarFixture{{name: "../escape", contents: []byte("bad")}}, false)
	if _, err := openTARBytesError(unsafe); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("unsafe error=%v", err)
	}
	truncated := makeTAR(t, []tarFixture{{name: "file", contents: bytes.Repeat([]byte("x"), 600)}}, false)
	truncated = truncated[:700]
	if _, err := openTARBytesError(truncated); !errors.Is(err, ErrCorruptArchive) {
		t.Fatalf("truncated error=%v", err)
	}
	badHeader := makeTAR(t, []tarFixture{{name: "file", contents: []byte("data"), format: archivetar.FormatUSTAR}}, false)
	badHeader[148] ^= 1
	if _, err := openTARBytesError(badHeader); !errors.Is(err, ErrCorruptArchive) {
		t.Fatalf("bad header error=%v", err)
	}
	tooMany := makeTAR(t, []tarFixture{{name: "one"}, {name: "two"}}, false)
	input := &fakeInput{data: tooMany}
	if _, err := (TARDriver{MaxEntries: 1}).Open(context.Background(), input); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("limit error=%v", err)
	}

	gzipData := makeTAR(t, []tarFixture{{name: "file", contents: []byte("contents")}}, true)
	gzipData[len(gzipData)-5] ^= 0xff
	if _, err := openTARBytesError(gzipData); !errors.Is(err, ErrCorruptArchive) {
		t.Fatalf("gzip checksum error=%v", err)
	}
	truncatedGzip := makeTAR(t, []tarFixture{{name: "file", contents: bytes.Repeat([]byte("x"), 4096)}}, true)
	truncatedGzip = truncatedGzip[:len(truncatedGzip)-4]
	if _, err := openTARBytesError(truncatedGzip); !errors.Is(err, ErrCorruptArchive) {
		t.Fatalf("truncated gzip error=%v", err)
	}
	var arbitrary bytes.Buffer
	gz := gzip.NewWriter(&arbitrary)
	_, _ = gz.Write([]byte("not a tar archive"))
	_ = gz.Close()
	if _, err := openTARBytesError(arbitrary.Bytes()); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("arbitrary gzip error=%v", err)
	}
}

func TestTAREmptyArchivesDuplicatesAndConflicts(t *testing.T) {
	t.Parallel()
	for _, compressed := range []bool{false, true} {
		session, _ := openTARBytes(t, makeTAR(t, nil, compressed))
		entries, err := session.ReadDir(context.Background(), "/")
		closeErr := session.Close()
		if err != nil || closeErr != nil || len(entries) != 0 {
			t.Fatalf("compressed=%v entries=%v errors=%v/%v", compressed, entries, err, closeErr)
		}
	}
	for name, fixtures := range map[string][]tarFixture{
		"duplicate":   {{name: "same"}, {name: "same"}},
		"file parent": {{name: "parent"}, {name: "parent/child"}},
	} {
		if _, err := openTARBytesError(makeTAR(t, fixtures, false)); !errors.Is(err, ErrCorruptArchive) {
			t.Errorf("%s error=%v", name, err)
		}
	}
}

func TestTARCreationRoundTripSelectionAndLimits(t *testing.T) {
	t.Parallel()
	for _, compressed := range []bool{false, true} {
		compressed := compressed
		t.Run(map[bool]string{false: "tar", true: "tar.gz"}[compressed], func(t *testing.T) {
			base := t.TempDir()
			source := filepath.Join(base, "Исходные файлы")
			if err := os.MkdirAll(filepath.Join(source, "nested", "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
			want := map[string][]byte{
				"файл с пробелами.txt": []byte("кириллица"),
				"nested/large.bin":     bytes.Repeat([]byte("0123456789abcdef"), 20_000),
			}
			for name, contents := range want {
				path := filepath.Join(source, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, contents, 0o640); err != nil {
					t.Fatal(err)
				}
			}
			destination := filepath.Join(base, map[bool]string{false: "result.tar", true: "result.tar.gz"}[compressed])
			var report TARCreationReport
			var err error
			if compressed {
				report, err = CreateTARGZ(context.Background(), localfs.LocalFileSystem{}, []string{source}, destination, TARCreationOptions{})
			} else {
				report, err = CreateTAR(context.Background(), localfs.LocalFileSystem{}, []string{source}, destination, TARCreationOptions{})
			}
			if err != nil || report.ArchivedFiles != 2 {
				t.Fatalf("report=%+v error=%v", report, err)
			}
			if _, lookupErr := exec.LookPath("tar"); lookupErr == nil {
				output, listErr := exec.Command("tar", "-tf", destination).CombinedOutput()
				if listErr != nil || !bytes.Contains(output, []byte("Исходные файлы/nested/empty/")) {
					t.Fatalf("system tar listing error=%v output=%q", listErr, output)
				}
			}
			session, err := NewStandardSource(FileInputOpener{}).Open(context.Background(), destination)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			extractRoot := filepath.Join(base, "extract")
			if err := os.Mkdir(extractRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			extractionDestination, err := NewLocalExtractionDestination(context.Background(), extractRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer extractionDestination.Close()
			extractionReport, err := NewDefaultExtractor().ExtractSelected(context.Background(), session,
				extractionDestination, nil, []string{"/Исходные файлы/файл с пробелами.txt"}, nil)
			if err != nil || extractionReport.BytesWritten != uint64(len(want["файл с пробелами.txt"])) {
				t.Fatalf("report=%+v error=%v", extractionReport, err)
			}
			actual, err := os.ReadFile(filepath.Join(extractRoot, "Исходные файлы", "файл с пробелами.txt"))
			if err != nil || !bytes.Equal(actual, want["файл с пробелами.txt"]) {
				t.Fatalf("contents match=%v error=%v", bytes.Equal(actual, want["файл с пробелами.txt"]), err)
			}
			limited, _ := NewExtractor(limits(100, 5, 100, 64, 1000))
			if _, err := limited.ExtractSelected(context.Background(), session, newFakeDestination(), nil,
				[]string{"/Исходные файлы/файл с пробелами.txt"}, nil); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("selection bypassed limit: %v", err)
			}
		})
	}
}

func TestTARGZExpansionRatioAndBoundedReads(t *testing.T) {
	t.Parallel()
	contents := bytes.Repeat([]byte{0}, 8<<20)
	data := makeTAR(t, []tarFixture{{name: "large.bin", contents: contents}}, true)
	input := &trackingArchiveInput{data: data}
	session, err := NewStandardSource(InputOpenerFunc(func(context.Context, string) (Input, error) {
		return input, nil
	})).Open(context.Background(), "large.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if input.largest > 256<<10 {
		t.Fatalf("largest ReaderAt request=%d", input.largest)
	}
	extractor, _ := NewExtractor(limits(10, 16<<20, 16<<20, 10, 10))
	if _, err := extractor.Extract(context.Background(), session, newFakeDestination(), nil); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("expansion ratio error=%v", err)
	}
}

func TestTARLargeEntryIndexAndCancellation(t *testing.T) {
	t.Parallel()
	fixtures := make([]tarFixture, 4000)
	for index := range fixtures {
		fixtures[index] = tarFixture{name: fmt.Sprintf("many/%05d.txt", index), contents: []byte("x")}
	}
	data := makeTAR(t, fixtures, true)
	session, _ := openTARBytes(t, data)
	defer session.Close()
	entries, err := session.ReadDir(context.Background(), "/many")
	if err != nil || len(entries) != len(fixtures) {
		t.Fatalf("entries=%d error=%v", len(entries), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.Open(ctx, "/many/03999.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open error=%v", err)
	}
}

func TestTARGZBatchExtractionUsesOnePass(t *testing.T) {
	for _, count := range []int{100, 1_000, 10_000} {
		count := count
		t.Run(fmt.Sprintf("%d-files", count), func(t *testing.T) {
			fixtures := make([]tarFixture, count)
			for index := range fixtures {
				fixtures[index] = tarFixture{name: fmt.Sprintf("many/%05d.txt", index), contents: []byte("x")}
			}
			data := makeTAR(t, fixtures, true)
			input := &trackingArchiveInput{data: data}
			session, err := NewStandardSource(InputOpenerFunc(func(context.Context, string) (Input, error) {
				return input, nil
			})).Open(context.Background(), "many.tar.gz")
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, ok := session.(ArchiveBatchSession); !ok {
				t.Fatal("TAR.GZ session does not expose batch streaming")
			}
			input.resetTracking()
			destination := newFakeDestination()
			destination.countOnly = true
			runtime.GC()
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			baseHeap, peakHeap := memory.HeapAlloc, memory.HeapAlloc
			lastSample := uint64(^uint64(0))
			started := time.Now()
			report, err := NewDefaultExtractor().ExtractSelected(context.Background(), session, destination, nil,
				[]string{"/many"}, func(progress operations.Progress) error {
					if count == 10_000 && progress.ProcessedFiles/100 != lastSample {
						lastSample = progress.ProcessedFiles / 100
						runtime.ReadMemStats(&memory)
						if memory.HeapAlloc > peakHeap {
							peakHeap = memory.HeapAlloc
						}
					}
					return nil
				})
			elapsed := time.Since(started)
			reads, bytesRead, starts := input.trackingStats()
			if err != nil || report.BytesWritten != uint64(count) || len(destination.files) != count {
				t.Fatalf("report=%+v files=%d error=%v", report, len(destination.files), err)
			}
			if starts != 1 {
				t.Fatalf("gzip passes=%d reads=%d bytes=%d", starts, reads, bytesRead)
			}
			if bytesRead > int64(len(data)) {
				t.Fatalf("read %d compressed bytes from %d-byte archive", bytesRead, len(data))
			}
			peakDelta := uint64(0)
			if peakHeap > baseHeap {
				peakDelta = peakHeap - baseHeap
			}
			if count == 10_000 && peakDelta > 128<<20 {
				t.Fatalf("extraction heap grew by %d bytes", peakDelta)
			}
			t.Logf("files=%d gzip_passes=%d compressed_bytes=%d elapsed=%s heap_delta=%d",
				count, starts, bytesRead, elapsed, peakDelta)
			if count == 1_000 {
				selected := make([]string, count)
				for index := range selected {
					selected[index] = fmt.Sprintf("/many/%05d.txt", index)
				}
				input.resetTracking()
				selectedDestination := newFakeDestination()
				selectedDestination.countOnly = true
				selectedReport, selectedErr := NewDefaultExtractor().ExtractSelected(context.Background(), session,
					selectedDestination, nil, selected, nil)
				_, selectedBytes, selectedStarts := input.trackingStats()
				if selectedErr != nil || selectedStarts != 1 || selectedReport.BytesWritten != uint64(count) {
					t.Fatalf("individual selection report=%+v passes=%d bytes=%d error=%v",
						selectedReport, selectedStarts, selectedBytes, selectedErr)
				}
			}
		})
	}
}

func TestTARGZBatchSelectionConflictAndScanCancellation(t *testing.T) {
	fixtures := make([]tarFixture, 2_000)
	for index := range fixtures {
		fixtures[index] = tarFixture{name: fmt.Sprintf("many/%05d.txt", index), contents: []byte("payload")}
	}
	data := makeTAR(t, fixtures, true)
	input := &trackingArchiveInput{data: data}
	session, err := NewStandardSource(InputOpenerFunc(func(context.Context, string) (Input, error) {
		return input, nil
	})).Open(context.Background(), "many.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	destination := newFakeDestination()
	destination.files["many/00000.txt"] = []byte("existing")
	input.resetTracking()
	report, err := NewDefaultExtractor().ExtractSelected(context.Background(), session, destination,
		ExtractionConflictResolverFunc(func(_ context.Context, conflict ExtractionConflict) (operations.ConflictAction, error) {
			if conflict.Source.Path != "/many/00000.txt" {
				t.Fatalf("unexpected conflict for %q", conflict.Source.Path)
			}
			return operations.Skip, nil
		}), []string{"/many/00000.txt", "/many/01999.txt"}, nil)
	_, _, starts := input.trackingStats()
	if err != nil || starts != 1 || len(report.Skipped) != 1 ||
		string(destination.files["many/00000.txt"]) != "existing" ||
		string(destination.files["many/01999.txt"]) != "payload" {
		t.Fatalf("report=%+v passes=%d files=%v error=%v", report, starts, destination.files, err)
	}

	cancelDestination := newFakeDestination()
	ctx, cancel := context.WithCancel(context.Background())
	input.resetTracking()
	input.cancelAfterBytes(int64(len(data))/3, cancel)
	_, err = NewDefaultExtractor().ExtractSelected(ctx, session, cancelDestination, nil,
		[]string{"/many/01999.txt"}, nil)
	reads, bytesRead, starts := input.trackingStats()
	if !errors.Is(err, context.Canceled) || starts != 1 || bytesRead >= int64(len(data)) ||
		len(cancelDestination.files) != 0 || cancelDestination.active != 0 {
		t.Fatalf("passes=%d reads=%d bytes=%d files=%v active=%d error=%v",
			starts, reads, bytesRead, cancelDestination.files, cancelDestination.active, err)
	}
}

func TestTARGZBatchWriteFailureAndCancellationDoNotPublish(t *testing.T) {
	data := makeTAR(t, []tarFixture{
		{name: "first", contents: bytes.Repeat([]byte("a"), extractionBufferSize*2)},
		{name: "second", contents: []byte("second")},
	}, true)
	for _, test := range []struct {
		name      string
		configure func(context.CancelFunc, *fakeDestination)
		want      error
	}{
		{name: "write failure", configure: func(_ context.CancelFunc, destination *fakeDestination) {
			destination.writeErrorAfter = 3
		}, want: errFakeWrite},
		{name: "cancellation", configure: func(cancel context.CancelFunc, destination *fakeDestination) {
			destination.cancel, destination.cancelOnWrite = cancel, true
		}, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, _ := openTARBytes(t, data)
			defer session.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			destination := newFakeDestination()
			test.configure(cancel, destination)
			report, err := NewDefaultExtractor().Extract(ctx, session, destination, nil)
			if !errors.Is(err, test.want) || len(destination.files) != 0 || destination.active != 0 ||
				len(report.Extracted) != 0 {
				t.Fatalf("report=%+v files=%v active=%d error=%v", report, destination.files, destination.active, err)
			}
		})
	}
}

func TestTARGZBatchRejectsChangedEntryBeforePublication(t *testing.T) {
	input := &fakeInput{data: makeTAR(t, []tarFixture{{name: "file", contents: []byte("original")}}, true)}
	session, err := NewStandardSource(InputOpenerFunc(func(context.Context, string) (Input, error) {
		return input, nil
	})).Open(context.Background(), "changed.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	replacement := makeTAR(t, []tarFixture{{name: "file", contents: []byte("different size")}}, true)
	input.mu.Lock()
	input.data = replacement
	input.mu.Unlock()
	destination := newFakeDestination()
	_, err = NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
	if !errors.Is(err, ErrCorruptArchive) || len(destination.files) != 0 || destination.active != 0 {
		t.Fatalf("files=%v active=%d error=%v", destination.files, destination.active, err)
	}
}

func TestZIPSessionDoesNotUseSequentialBatchPath(t *testing.T) {
	session, _ := openZIPBytes(t, makeZIP(t, []zipFixture{{name: "file", contents: []byte("data")}}))
	defer session.Close()
	if _, ok := session.(ArchiveBatchSession); ok {
		t.Fatal("ZIP unexpectedly uses sequential batch extraction")
	}
}

func TestTARCreationCancellationConflictAndLinksPreserveData(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	destination := filepath.Join(base, "archive.tar.gz")
	if err := os.WriteFile(source, bytes.Repeat([]byte("x"), extractionBufferSize*3), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := CreateTARGZ(ctx, localfs.LocalFileSystem{}, []string{source}, destination, TARCreationOptions{
		ConflictResolver: TARCreationConflictResolverFunc(func(context.Context, TARCreationConflict) (operations.ConflictAction, error) {
			return operations.Replace, nil
		}),
		Progress: func(operations.Progress) error { cancel(); return ctx.Err() },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	assertFileContents(t, destination, "original")
	assertNoCopyTemporaries(t, base)

	link := filepath.Join(base, "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTAR(context.Background(), localfs.LocalFileSystem{}, []string{link}, filepath.Join(base, "link.tar"), TARCreationOptions{}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("link error=%v", err)
	}
	directory := filepath.Join(base, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(directory, "nested-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTAR(context.Background(), localfs.LocalFileSystem{}, []string{directory},
		filepath.Join(base, "nested-link.tar"), TARCreationOptions{}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("nested link error=%v", err)
	}
	if command, lookupErr := exec.LookPath("mkfifo"); lookupErr == nil {
		fifo := filepath.Join(base, "fifo")
		if output, commandErr := exec.Command(command, fifo).CombinedOutput(); commandErr != nil {
			t.Fatalf("mkfifo: %v: %s", commandErr, output)
		}
		if _, err := CreateTAR(context.Background(), localfs.LocalFileSystem{}, []string{fifo},
			filepath.Join(base, "fifo.tar"), TARCreationOptions{}); !errors.Is(err, ErrUnsupportedEntry) {
			t.Fatalf("FIFO error=%v", err)
		}
	}

	lateDestination := filepath.Join(base, "late.tar")
	created := false
	report, err := CreateTAR(context.Background(), localfs.LocalFileSystem{}, []string{source}, lateDestination, TARCreationOptions{
		Progress: func(operations.Progress) error {
			if !created {
				created = true
				return os.WriteFile(lateDestination, []byte("concurrent"), 0o600)
			}
			return nil
		},
		ConflictResolver: TARCreationConflictResolverFunc(func(context.Context, TARCreationConflict) (operations.ConflictAction, error) {
			return operations.Skip, nil
		}),
	})
	if err != nil || !report.Skipped {
		t.Fatalf("late report=%+v error=%v", report, err)
	}
	assertFileContents(t, lateDestination, "concurrent")
	assertNoCopyTemporaries(t, base)
}

func TestTARCreationIOCloseAndSyncErrorsPreserveDestination(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.WriteFile(source, []byte("source data"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := TARCreationConflictResolverFunc(func(context.Context, TARCreationConflict) (operations.ConflictAction, error) {
		return operations.Replace, nil
	})
	for _, test := range []struct {
		name       string
		filesystem TARCreateFileSystem
		want       error
	}{
		{name: "open", filesystem: failingZIPCreationFS{LocalFileSystem: localfs.LocalFileSystem{}, openErr: errAcceptanceOpen}, want: errAcceptanceOpen},
		{name: "sync", filesystem: failingTARTemporaryFS{syncErr: errAcceptanceSync}, want: errAcceptanceSync},
		{name: "close", filesystem: failingTARTemporaryFS{closeErr: errAcceptanceClose}, want: errAcceptanceClose},
		{name: "publish", filesystem: failingZIPCreationFS{LocalFileSystem: localfs.LocalFileSystem{}, publishReplaceErr: errAcceptancePublish}, want: errAcceptancePublish},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(base, test.name+".tar")
			if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := CreateTAR(context.Background(), test.filesystem, []string{source}, destination,
				TARCreationOptions{ConflictResolver: resolver})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			assertFileContents(t, destination, "existing")
			assertNoCopyTemporaries(t, base)
		})
	}
	destination := filepath.Join(base, "bad-level.tar.gz")
	if _, err := CreateTARGZ(context.Background(), localfs.LocalFileSystem{}, []string{source}, destination,
		TARCreationOptions{GzipLevel: 99}); err == nil {
		t.Fatal("invalid gzip level succeeded")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid gzip destination appeared: %v", err)
	}
	assertNoCopyTemporaries(t, base)
}

func TestTARCreationEmptyArchives(t *testing.T) {
	t.Parallel()
	for _, compressed := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), map[bool]string{false: "empty.tar", true: "empty.tar.gz"}[compressed])
		var report TARCreationReport
		var err error
		if compressed {
			report, err = CreateTARGZ(context.Background(), localfs.LocalFileSystem{}, nil, path, TARCreationOptions{})
		} else {
			report, err = CreateTAR(context.Background(), localfs.LocalFileSystem{}, nil, path, TARCreationOptions{})
		}
		if err != nil || report.ArchivedFiles != 0 || report.ArchivedBytes != 0 {
			t.Fatalf("compressed=%v report=%+v error=%v", compressed, report, err)
		}
		session, err := NewStandardSource(FileInputOpener{}).Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		entries, readErr := session.ReadDir(context.Background(), "/")
		closeErr := session.Close()
		if readErr != nil || closeErr != nil || len(entries) != 0 {
			t.Fatalf("entries=%v errors=%v/%v", entries, readErr, closeErr)
		}
	}
}

func TestTARFileInputSessionsDoNotLeakDescriptors(t *testing.T) {
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("descriptor inspection unavailable: %v", err)
	}
	for _, compressed := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), map[bool]string{false: "archive.tar", true: "archive.tar.gz"}[compressed])
		if err := os.WriteFile(path, makeTAR(t, []tarFixture{{name: "file", contents: []byte("data")}}, compressed), 0o600); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 30; index++ {
			session, err := NewStandardSource(FileInputOpener{}).Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := session.Open(context.Background(), "/file")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, stream); err != nil {
				t.Fatal(err)
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before)+1 {
		t.Fatalf("file descriptors grew from %d to %d", len(before), len(after))
	}
}

func TestTARExternalInteroperability(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("system tar unavailable")
	}
	base := t.TempDir()
	source := filepath.Join(base, "external")
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "файл с пробелами.txt"), []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, compressed := range []bool{false, true} {
		name := map[bool]string{false: "external.tar", true: "external.tar.gz"}[compressed]
		archivePath := filepath.Join(base, name)
		args := []string{"-cf", archivePath, "-C", base, "external"}
		if compressed {
			args[0] = "-czf"
		}
		if output, err := exec.Command("tar", args...).CombinedOutput(); err != nil {
			t.Fatalf("system tar: %v: %s", err, output)
		}
		session, err := NewStandardSource(FileInputOpener{}).Open(context.Background(), archivePath)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := session.Open(context.Background(), "/external/файл с пробелами.txt")
		if err != nil {
			_ = session.Close()
			t.Fatal(err)
		}
		contents, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		sessionErr := session.Close()
		if readErr != nil || closeErr != nil || sessionErr != nil || string(contents) != "external" {
			t.Fatalf("contents=%q errors=%v/%v/%v", contents, readErr, closeErr, sessionErr)
		}
	}
}

func makeTAR(t *testing.T, fixtures []tarFixture, compressed bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var output io.Writer = &buffer
	var gzipWriter *gzip.Writer
	if compressed {
		gzipWriter = gzip.NewWriter(&buffer)
		output = gzipWriter
	}
	writer := archivetar.NewWriter(output)
	for _, fixture := range fixtures {
		typeflag := fixture.typeflag
		if typeflag == 0 {
			typeflag = archivetar.TypeReg
		}
		mode := fixture.mode
		if mode == 0 {
			mode = 0o600
		}
		header := &archivetar.Header{Name: fixture.name, Typeflag: typeflag, Linkname: fixture.linkname,
			Mode: mode, ModTime: fixture.modified, Format: fixture.format}
		if typeflag == archivetar.TypeReg || typeflag == archivetar.TypeRegA {
			header.Size = int64(len(fixture.contents))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(fixture.contents) > 0 {
			if _, err := writer.Write(fixture.contents); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if gzipWriter != nil {
		if err := gzipWriter.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buffer.Bytes()
}

func openTARBytes(t *testing.T, data []byte) (ArchiveSession, *fakeInput) {
	t.Helper()
	input := &fakeInput{data: slices.Clone(data)}
	session, err := NewStandardSource(InputOpenerFunc(func(context.Context, string) (Input, error) { return input, nil })).Open(context.Background(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	return session, input
}

func openTARBytesError(data []byte) (ArchiveSession, error) {
	input := &fakeInput{data: slices.Clone(data)}
	return NewStandardSource(InputOpenerFunc(func(context.Context, string) (Input, error) { return input, nil })).Open(context.Background(), "archive")
}

type trackingArchiveInput struct {
	mu       sync.Mutex
	data     []byte
	largest  int
	closed   bool
	reads    int
	bytes    int64
	starts   int
	cancel   context.CancelFunc
	cancelAt int64
}

func (input *trackingArchiveInput) ReadAt(buffer []byte, offset int64) (int, error) {
	input.mu.Lock()
	defer input.mu.Unlock()
	if len(buffer) > input.largest {
		input.largest = len(buffer)
	}
	if offset >= int64(len(input.data)) {
		return 0, io.EOF
	}
	n := copy(buffer, input.data[offset:])
	input.reads++
	input.bytes += int64(n)
	if offset == 0 {
		input.starts++
	}
	if input.cancel != nil && input.bytes >= input.cancelAt {
		input.cancel()
		input.cancel = nil
	}
	if n != len(buffer) {
		return n, io.EOF
	}
	return n, nil
}

func (input *trackingArchiveInput) Size() int64 { return int64(len(input.data)) }
func (input *trackingArchiveInput) Close() error {
	input.mu.Lock()
	defer input.mu.Unlock()
	input.closed = true
	return nil
}

func (input *trackingArchiveInput) resetTracking() {
	input.mu.Lock()
	defer input.mu.Unlock()
	input.reads, input.bytes, input.starts = 0, 0, 0
	input.cancel, input.cancelAt = nil, 0
}

func (input *trackingArchiveInput) trackingStats() (int, int64, int) {
	input.mu.Lock()
	defer input.mu.Unlock()
	return input.reads, input.bytes, input.starts
}

func (input *trackingArchiveInput) cancelAfterBytes(amount int64, cancel context.CancelFunc) {
	input.mu.Lock()
	defer input.mu.Unlock()
	input.cancelAt, input.cancel = amount, cancel
}

var (
	errAcceptanceSync  = errors.New("acceptance sync failure")
	errAcceptanceClose = errors.New("acceptance close failure")
)

type failingTARTemporaryFS struct {
	localfs.LocalFileSystem
	syncErr  error
	closeErr error
}

func (filesystem failingTARTemporaryFS) CreateTemporaryFile(ctx context.Context, directory string,
	mode fs.FileMode) (core.TemporaryFile, error) {
	file, err := filesystem.LocalFileSystem.CreateTemporaryFile(ctx, directory, mode)
	if err != nil {
		return nil, err
	}
	return &failingTARTemporaryFile{TemporaryFile: file, syncErr: filesystem.syncErr,
		closeErr: filesystem.closeErr}, nil
}

type failingTARTemporaryFile struct {
	core.TemporaryFile
	syncErr  error
	closeErr error
}

func (file *failingTARTemporaryFile) Sync() error {
	if file.syncErr != nil {
		return file.syncErr
	}
	return file.TemporaryFile.Sync()
}

func (file *failingTARTemporaryFile) Close() error {
	return errors.Join(file.TemporaryFile.Close(), file.closeErr)
}
