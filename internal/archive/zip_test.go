package archive

import (
	archivezip "archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

func TestZIPDriverDetectionMetadataAndStreaming(t *testing.T) {
	t.Parallel()
	modified := time.Date(2026, time.November, 26, 12, 30, 0, 0, time.UTC)
	data := makeZIP(t, []zipFixture{
		{name: "empty/", directory: true, modified: modified},
		{name: "nested/файл с пробелами.txt", contents: []byte("данные"), modified: modified},
	})
	session, input := openZIPBytes(t, data)
	root, err := session.ReadDir(context.Background(), "/")
	if err != nil || !reflect.DeepEqual(entryNames(root), []string{"empty", "nested"}) {
		t.Fatalf("root=%+v error=%v", root, err)
	}
	entry, err := session.Stat(context.Background(), "/nested/файл с пробелами.txt")
	if err != nil || entry.RawPath != "nested/файл с пробелами.txt" || entry.Size == nil || *entry.Size != int64(len([]byte("данные"))) ||
		entry.CompressedSize == nil || entry.ModifiedAt == nil || entry.Extra["zip.method"] == "" {
		t.Fatalf("entry=%+v error=%v", entry, err)
	}
	stream, err := session.Open(context.Background(), entry.Path)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(stream)
	if closeErr := stream.Close(); err == nil {
		err = closeErr
	}
	if err != nil || string(contents) != "данные" {
		t.Fatalf("contents=%q error=%v", contents, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if input.closeCount() != 1 {
		t.Fatalf("input close count=%d", input.closeCount())
	}
}

func TestZIPProbeUsesBoundedReaderAtCalls(t *testing.T) {
	t.Parallel()
	input := &boundedZIPProbeInput{size: 8 << 30, maximum: 22 + (1 << 16) - 1}
	match, err := (ZIPDriver{}).Probe(context.Background(), input)
	if err != nil || match != DefiniteMatch {
		t.Fatalf("match=%v error=%v", match, err)
	}
	if input.largest > input.maximum || input.reads > 2 {
		t.Fatalf("probe reads=%d largest=%d maximum=%d", input.reads, input.largest, input.maximum)
	}
}

func TestZIPDriverRejectsOversizedCentralDirectoryBeforeIndexing(t *testing.T) {
	t.Parallel()
	input := &boundedZIPProbeInput{size: 8 << 30, maximum: 22 + (1 << 16) - 1, entryCount: 20_000}
	backend, err := (ZIPDriver{MaxEntries: 10_000}).Open(context.Background(), input)
	if !errors.Is(err, ErrLimitExceeded) || backend != nil {
		t.Fatalf("backend=%v error=%v", backend, err)
	}
	if input.reads != 1 || input.largest > input.maximum {
		t.Fatalf("reads=%d largest=%d", input.reads, input.largest)
	}
}

func TestZIPDriverEmptyPrefixZIP64AndRegistration(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{
		"empty":  makeZIP(t, nil),
		"prefix": append([]byte("self extracting prefix"), makeZIP(t, []zipFixture{{name: "file", contents: []byte("ok")}})...),
		"zip64":  decodeBase64(t, zip64Fixture),
	} {
		name, data := name, data
		t.Run(name, func(t *testing.T) {
			session, _ := openZIPBytes(t, data)
			defer session.Close()
			if session.Format() != ZIPFormat {
				t.Fatalf("format=%q", session.Format())
			}
			entries, err := session.ReadDir(context.Background(), "/")
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) > 0 && entries[0].Kind == EntryFile {
				stream, err := session.Open(context.Background(), entries[0].Path)
				if err != nil {
					t.Fatal(err)
				}
				contents, readErr := io.ReadAll(stream)
				closeErr := stream.Close()
				if readErr != nil || closeErr != nil || len(contents) == 0 {
					t.Fatalf("ZIP64/prefix stream length=%d read=%v close=%v", len(contents), readErr, closeErr)
				}
			}
		})
	}
	registry := NewRegistry()
	if err := RegisterZIP(registry); err != nil {
		t.Fatal(err)
	}
	if err := RegisterZIP(registry); !errors.Is(err, ErrFormatRegistered) {
		t.Fatalf("duplicate registration error=%v", err)
	}
}

func TestZIPCreationSupportsEmptyArchive(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "empty.zip")
	report, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, nil, path, ZIPCreationOptions{})
	if err != nil || report.ArchivedFiles != 0 || report.ArchivedBytes != 0 {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	session, err := NewZIPSource(FileInputOpener{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	entries, err := session.ReadDir(context.Background(), "/")
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v error=%v", entries, err)
	}
}

func TestZIPDriverRejectsCorruptEncryptedUnknownAndDuplicate(t *testing.T) {
	t.Parallel()
	valid := makeZIP(t, []zipFixture{{name: "file", contents: []byte("contents"), store: true}})
	tests := map[string]struct {
		data []byte
		want error
	}{
		"truncated":       {data: valid[:len(valid)/2], want: ErrCorruptArchive},
		"encrypted":       {data: patchZIPFlags(valid, 1), want: ErrEncrypted},
		"unknown method":  {data: patchZIPMethod(valid, 99), want: ErrUnsupportedEntry},
		"duplicate paths": {data: makeZIP(t, []zipFixture{{name: "same", contents: []byte("a")}, {name: "same", contents: []byte("b")}}), want: ErrCorruptArchive},
	}
	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			_, err := NewZIPSource(InputOpenerFunc(func(context.Context, string) (Input, error) {
				return &fakeInput{data: slices.Clone(test.data)}, nil
			})).Open(context.Background(), name)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestZIPCentralAndLocalHeaderCorruption(t *testing.T) {
	t.Parallel()
	valid := makeZIP(t, []zipFixture{{name: "safe.txt", contents: []byte("contents"), store: true}})

	central := slices.Clone(valid)
	centralOffset := bytes.Index(central, []byte{'P', 'K', 1, 2})
	if centralOffset < 0 {
		t.Fatal("central directory not found")
	}
	central[centralOffset] ^= 0xff
	input := &fakeInput{data: central}
	_, err := NewZIPSource(InputOpenerFunc(func(context.Context, string) (Input, error) { return input, nil })).Open(context.Background(), "central.zip")
	if !errors.Is(err, ErrCorruptArchive) || input.closeCount() != 1 {
		t.Fatalf("central error=%v close count=%d", err, input.closeCount())
	}

	localSignature := slices.Clone(valid)
	localSignature[0] ^= 0xff
	localInput := &fakeInput{data: localSignature}
	session, err := NewZIPSource(InputOpenerFunc(func(context.Context, string) (Input, error) {
		return localInput, nil
	})).Open(context.Background(), "local.zip")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := session.Open(context.Background(), "/safe.txt")
	if !errors.Is(err, ErrCorruptArchive) || stream != nil {
		t.Fatalf("local-header stream=%v error=%v", stream, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if localInput.closeCount() != 1 {
		t.Fatalf("local-header input close count=%d", localInput.closeCount())
	}

	// The standard library intentionally treats the central directory as the
	// authoritative name. A dangerous local-header name therefore cannot
	// influence ArchiveEntry.RawPath or the extraction destination.
	localNameMismatch := slices.Clone(valid)
	copy(localNameMismatch[30:30+len("safe.txt")], []byte("../x.txt"))
	mismatchSession, err := openZIPBytesError(localNameMismatch)
	if err != nil {
		t.Fatal(err)
	}
	defer mismatchSession.Close()
	entry, err := mismatchSession.Stat(context.Background(), "/safe.txt")
	if err != nil || entry.RawPath != "safe.txt" {
		t.Fatalf("entry=%+v error=%v", entry, err)
	}
	reader, err := mismatchSession.Open(context.Background(), entry.Path)
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(contents) != "contents" {
		t.Fatalf("contents=%q read=%v close=%v", contents, readErr, closeErr)
	}
}

func TestZIPManyEntriesAndSelectionCannotBypassLimits(t *testing.T) {
	const entryCount = 20_000
	fixtures := make([]zipFixture, entryCount)
	for index := range fixtures {
		fixtures[index] = zipFixture{name: fmt.Sprintf("many/%05d.txt", index), contents: []byte("x"), store: true}
	}
	data := makeZIP(t, fixtures)
	if backend, err := (ZIPDriver{MaxEntries: 10_000}).Open(context.Background(), &fakeInput{data: data}); !errors.Is(err, ErrLimitExceeded) || backend != nil {
		t.Fatalf("driver backend=%v error=%v", backend, err)
	}
	session, _ := openZIPBytes(t, data)
	defer session.Close()
	entries, err := session.ReadDir(context.Background(), "/many")
	if err != nil || len(entries) != entryCount {
		t.Fatalf("entry count=%d error=%v", len(entries), err)
	}
	extractor, err := NewExtractor(limits(10_000, 10, 100_000, 10, 100))
	if err != nil {
		t.Fatal(err)
	}
	destination := newFakeDestination()
	_, err = extractor.ExtractSelected(context.Background(), session, destination, nil, []string{"/many/00000.txt"}, nil)
	if !errors.Is(err, ErrLimitExceeded) || len(destination.files) != 0 || destination.active != 0 {
		t.Fatalf("files=%d active=%d error=%v", len(destination.files), destination.active, err)
	}
}

func TestZIPArchivesFromExternalTools(t *testing.T) {
	for _, tool := range []struct {
		name string
		args func(string) []string
	}{
		{name: "Info-ZIP", args: func(destination string) []string { return []string{"-q", "-r", destination, "."} }},
		{name: "7-Zip", args: func(destination string) []string { return []string{"a", "-bd", "-y", "-tzip", destination, "."} }},
	} {
		tool := tool
		t.Run(tool.name, func(t *testing.T) {
			binary := "zip"
			if tool.name == "7-Zip" {
				binary = "7z"
			}
			if _, err := exec.LookPath(binary); err != nil {
				t.Skipf("%s is unavailable: %v", binary, err)
			}
			base := t.TempDir()
			source := filepath.Join(base, "source")
			if err := os.MkdirAll(filepath.Join(source, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
			want := []byte("сторонний ZIP")
			if err := os.WriteFile(filepath.Join(source, "файл с пробелами.txt"), want, 0o600); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(base, binary+".zip")
			command := exec.Command(binary, tool.args(archivePath)...)
			command.Dir = source
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%s failed: %v: %s", binary, err, output)
			}
			verifyExternalZIP(t, archivePath, want)
		})
	}
}

func TestZIPCRCFailureDoesNotPublishOrLeaveTemporary(t *testing.T) {
	t.Parallel()
	contents := []byte("checksum payload unique marker")
	data := makeZIP(t, []zipFixture{{name: "file.txt", contents: contents, store: true}})
	position := bytes.Index(data, contents)
	if position < 0 {
		t.Fatal("stored ZIP payload not found")
	}
	data[position] ^= 0xff
	session, _ := openZIPBytes(t, data)
	defer session.Close()
	root := t.TempDir()
	destination, err := NewLocalExtractionDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	_, err = NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
	if !errors.Is(err, ErrCorruptArchive) {
		t.Fatalf("error=%v", err)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("destination entries=%v error=%v", entries, readErr)
	}
}

func TestZIPExtractionCancellationCleansTemporary(t *testing.T) {
	t.Parallel()
	data := makeZIP(t, []zipFixture{{name: "large.bin", contents: bytes.Repeat([]byte("x"), extractionBufferSize*3), store: true}})
	session, _ := openZIPBytes(t, data)
	defer session.Close()
	root := t.TempDir()
	destination, err := NewLocalExtractionDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	ctx, cancel := context.WithCancel(context.Background())
	_, err = NewDefaultExtractor().ExtractSelected(ctx, session, destination, nil, []string{"/large.bin"},
		func(progress operations.Progress) error {
			if progress.ProcessedBytes > 0 {
				cancel()
			}
			return ctx.Err()
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("destination entries=%v error=%v", entries, readErr)
	}
}

func TestZIPRoundTripCreateOpenExtract(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	source := filepath.Join(base, "Исходные файлы")
	if err := os.MkdirAll(filepath.Join(source, "nested", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		"файл с пробелами.txt": []byte("кириллица"),
		"nested/large.bin":     bytes.Repeat([]byte("0123456789abcdef"), 128*1024),
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
	archivePath := filepath.Join(base, "результат.zip")
	var creationProgress []operations.Progress
	report, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{source}, archivePath,
		ZIPCreationOptions{Progress: func(value operations.Progress) error {
			creationProgress = append(creationProgress, value)
			return nil
		}})
	if err != nil || report.ArchivedFiles != 2 || report.ArchivedBytes != uint64(len(want["файл с пробелами.txt"])+len(want["nested/large.bin"])) {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	if len(creationProgress) < 2 || creationProgress[len(creationProgress)-1].ProcessedFiles != 2 {
		t.Fatalf("progress=%+v", creationProgress)
	}

	session, err := NewZIPSource(FileInputOpener{}).Open(context.Background(), archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	extractRoot := filepath.Join(base, "extracted")
	if err := os.Mkdir(extractRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	destination, err := NewLocalExtractionDestination(context.Background(), extractRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	var extractionProgress []operations.Progress
	extractionReport, err := NewDefaultExtractor().ExtractSelected(context.Background(), session, destination, nil,
		[]string{"/Исходные файлы"}, func(value operations.Progress) error {
			extractionProgress = append(extractionProgress, value)
			return nil
		})
	if err != nil || extractionReport.BytesWritten != report.ArchivedBytes {
		t.Fatalf("report=%+v error=%v", extractionReport, err)
	}
	for name, expected := range want {
		actual, err := os.ReadFile(filepath.Join(extractRoot, "Исходные файлы", filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("%s identical=%v error=%v", name, bytes.Equal(actual, expected), err)
		}
	}
	if info, err := os.Stat(filepath.Join(extractRoot, "Исходные файлы", "nested", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory info=%v error=%v", info, err)
	}
	if len(extractionProgress) == 0 || extractionProgress[len(extractionProgress)-1].ProcessedFiles != 2 {
		t.Fatalf("progress=%+v", extractionProgress)
	}
}

func TestZIPSelectiveExtractionAndLimits(t *testing.T) {
	t.Parallel()
	data := makeZIP(t, []zipFixture{
		{name: "selected/a", contents: []byte("aaa")},
		{name: "selected/b", contents: []byte("bbb")},
		{name: "other", contents: []byte("other")},
	})
	session, _ := openZIPBytes(t, data)
	defer session.Close()
	destination := newFakeDestination()
	report, err := NewDefaultExtractor().ExtractSelected(context.Background(), session, destination, nil,
		[]string{"/selected/a"}, nil)
	if err != nil || string(destination.files["selected/a"]) != "aaa" || len(destination.files) != 1 ||
		!slices.Contains(report.Extracted, "selected") {
		t.Fatalf("report=%+v files=%v error=%v", report, destination.files, err)
	}
	multipleSession, _ := openZIPBytes(t, data)
	defer multipleSession.Close()
	multipleDestination := newFakeDestination()
	if _, err := NewDefaultExtractor().ExtractSelected(context.Background(), multipleSession, multipleDestination, nil,
		[]string{"/selected/b", "/other"}, nil); err != nil || len(multipleDestination.files) != 2 ||
		string(multipleDestination.files["selected/b"]) != "bbb" || string(multipleDestination.files["other"]) != "other" {
		t.Fatalf("multiple files=%v error=%v", multipleDestination.files, err)
	}

	for name, configured := range map[string]ExtractionLimits{
		"size":  limits(10, 2, 100, 10, 100),
		"count": limits(2, 100, 100, 10, 100),
	} {
		name, configured := name, configured
		t.Run(name, func(t *testing.T) {
			s, _ := openZIPBytes(t, data)
			defer s.Close()
			extractor, _ := NewExtractor(configured)
			if _, err := extractor.Extract(context.Background(), s, newFakeDestination(), nil); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestZIPExtractionConflictUsesSafePublication(t *testing.T) {
	t.Parallel()
	data := makeZIP(t, []zipFixture{{name: "file", contents: []byte("new")}})
	session, _ := openZIPBytes(t, data)
	defer session.Close()
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination, err := NewLocalExtractionDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	_, err = NewDefaultExtractor().Extract(context.Background(), session, destination,
		ExtractionConflictResolverFunc(func(context.Context, ExtractionConflict) (operations.ConflictAction, error) {
			return operations.Replace, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	assertZIPFileContents(t, path, "new")
	assertNoExtractionTemporaries(t, root)
}

func TestZIPUnsafeRawPathsAndDestinationSymlink(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"../escape", "/tmp/escape", `C:\escape`, `\\server\share\escape`} {
		name := name
		t.Run(name, func(t *testing.T) {
			data := makeZIP(t, []zipFixture{{name: name, contents: []byte("bad")}})
			session, err := openZIPBytesError(data)
			if err == nil {
				defer session.Close()
				_, err = NewDefaultExtractor().Extract(context.Background(), session, newFakeDestination(), nil)
			}
			if !errors.Is(err, ErrUnsafePath) && !errors.Is(err, ErrCorruptArchive) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	data := makeZIP(t, []zipFixture{{name: "linked/file", contents: []byte("bad")}})
	session, _ := openZIPBytes(t, data)
	defer session.Close()
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	destination, err := NewLocalExtractionDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if _, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("error=%v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside=%v error=%v", entries, err)
	}
}

func TestZIPArchiveLinksAndSpecialEntriesAreNotExtracted(t *testing.T) {
	t.Parallel()
	for name, mode := range map[string]fs.FileMode{
		"symlink": os.ModeSymlink | 0o777,
		"special": os.ModeNamedPipe | 0o600,
	} {
		name, mode := name, mode
		t.Run(name, func(t *testing.T) {
			data := makeZIP(t, []zipFixture{{name: name, contents: []byte("target"), mode: mode}})
			session, _ := openZIPBytes(t, data)
			defer session.Close()
			if _, err := NewDefaultExtractor().Extract(context.Background(), session, newFakeDestination(), nil); !errors.Is(err, ErrUnsupportedEntry) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestZIPCreationConflictCancellationAndSelfExclusion(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "file"), bytes.Repeat([]byte("x"), extractionBufferSize*3), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(source, "archive.zip")
	if err := os.WriteFile(destination, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := CreateZIP(ctx, localfs.LocalFileSystem{}, []string{source}, destination, ZIPCreationOptions{
		ConflictResolver: ZIPCreationConflictResolverFunc(func(context.Context, ZIPCreationConflict) (operations.ConflictAction, error) {
			return operations.Replace, nil
		}),
		Progress: func(operations.Progress) error {
			cancel()
			return ctx.Err()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	assertZIPFileContents(t, destination, "original")
	assertNoCopyTemporaries(t, source)

	report, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{source}, destination, ZIPCreationOptions{
		ConflictResolver: ZIPCreationConflictResolverFunc(func(context.Context, ZIPCreationConflict) (operations.ConflictAction, error) {
			return operations.Replace, nil
		}),
	})
	if err != nil || report.ArchivedFiles != 1 {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	session, err := NewZIPSource(FileInputOpener{}).Open(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	entries, err := session.ReadDir(context.Background(), "/source")
	if err != nil || !reflect.DeepEqual(entryNames(entries), []string{"file"}) {
		t.Fatalf("entries=%v error=%v", entryNames(entries), err)
	}
	if _, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{destination}, destination, ZIPCreationOptions{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("self archive error=%v", err)
	}
}

func TestZIPCreationRejectsLinksAndPreservesExistingOnConflict(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	file := filepath.Join(base, "file")
	link := filepath.Join(base, "link")
	destination := filepath.Join(base, "archive.zip")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{link}, destination, ZIPCreationOptions{}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("symlink error=%v", err)
	}
	outside := filepath.Join(base, "outside")
	outputLink := filepath.Join(base, "output-link.zip")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, outputLink); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{file}, outputLink, ZIPCreationOptions{
		ConflictResolver: ZIPCreationConflictResolverFunc(func(context.Context, ZIPCreationConflict) (operations.ConflictAction, error) {
			return operations.Replace, nil
		}),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("output symlink error=%v", err)
	}
	assertZIPFileContents(t, outside, "outside")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{file}, destination, ZIPCreationOptions{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	assertZIPFileContents(t, destination, "old")
	report, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{file}, destination, ZIPCreationOptions{
		ConflictResolver: ZIPCreationConflictResolverFunc(func(context.Context, ZIPCreationConflict) (operations.ConflictAction, error) {
			return operations.Skip, nil
		}),
	})
	if err != nil || !report.Skipped {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	assertZIPFileContents(t, destination, "old")
}

func TestZIPCreationLateConflictDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	destination := filepath.Join(base, "archive.zip")
	if err := os.WriteFile(source, bytes.Repeat([]byte("x"), extractionBufferSize*2), 0o600); err != nil {
		t.Fatal(err)
	}
	created := false
	resolverCalls := 0
	report, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{source}, destination, ZIPCreationOptions{
		Progress: func(operations.Progress) error {
			if !created {
				created = true
				return os.WriteFile(destination, []byte("concurrent"), 0o600)
			}
			return nil
		},
		ConflictResolver: ZIPCreationConflictResolverFunc(func(context.Context, ZIPCreationConflict) (operations.ConflictAction, error) {
			resolverCalls++
			return operations.Skip, nil
		}),
	})
	if err != nil || !report.Skipped || resolverCalls != 1 {
		t.Fatalf("report=%+v calls=%d error=%v", report, resolverCalls, err)
	}
	assertZIPFileContents(t, destination, "concurrent")
	assertNoCopyTemporaries(t, base)
}

func TestZIPCreationRejectsConcurrentDestinationReplacement(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	destination := filepath.Join(base, "archive.zip")
	if err := os.WriteFile(source, bytes.Repeat([]byte("x"), extractionBufferSize*2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaced := false
	_, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{source}, destination, ZIPCreationOptions{
		ConflictResolver: ZIPCreationConflictResolverFunc(func(context.Context, ZIPCreationConflict) (operations.ConflictAction, error) {
			return operations.Replace, nil
		}),
		Progress: func(operations.Progress) error {
			if replaced {
				return nil
			}
			replaced = true
			concurrent := filepath.Join(base, "concurrent")
			if err := os.WriteFile(concurrent, []byte("concurrent"), 0o600); err != nil {
				return err
			}
			return os.Rename(concurrent, destination)
		},
	})
	if !errors.Is(err, core.ErrDestinationChanged) {
		t.Fatalf("error=%v", err)
	}
	assertZIPFileContents(t, destination, "concurrent")
	assertNoCopyTemporaries(t, base)
}

func TestZIPCreationIOAndPublicationErrorsPreserveDestination(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.WriteFile(source, []byte("source data"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := ZIPCreationConflictResolverFunc(func(context.Context, ZIPCreationConflict) (operations.ConflictAction, error) {
		return operations.Replace, nil
	})
	for _, test := range []struct {
		name       string
		filesystem ZIPCreateFileSystem
		existing   bool
		want       error
	}{
		{name: "source open", filesystem: failingZIPCreationFS{LocalFileSystem: localfs.LocalFileSystem{}, openErr: errAcceptanceOpen}, existing: true, want: errAcceptanceOpen},
		{name: "publish new", filesystem: failingZIPCreationFS{LocalFileSystem: localfs.LocalFileSystem{}, publishNewErr: errAcceptancePublish}, want: errAcceptancePublish},
		{name: "publish replace", filesystem: failingZIPCreationFS{LocalFileSystem: localfs.LocalFileSystem{}, publishReplaceErr: errAcceptancePublish}, existing: true, want: errAcceptancePublish},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(base, strings.ReplaceAll(test.name, " ", "-")+".zip")
			if test.existing {
				if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := CreateZIP(context.Background(), test.filesystem, []string{source}, destination,
				ZIPCreationOptions{ConflictResolver: resolver})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if test.existing {
				assertZIPFileContents(t, destination, "existing")
			} else if _, statErr := os.Lstat(destination); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("destination appeared: %v", statErr)
			}
			assertNoCopyTemporaries(t, base)
		})
	}
}

func TestZIPFileInputSessionsDoNotLeakDescriptors(t *testing.T) {
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("descriptor inspection unavailable: %v", err)
	}
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, makeZIP(t, []zipFixture{{name: "file", contents: []byte("data")}}), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 50; index++ {
		session, err := NewZIPSource(FileInputOpener{}).Open(context.Background(), path)
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
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before)+1 {
		t.Fatalf("file descriptors grew from %d to %d", len(before), len(after))
	}
}

type zipFixture struct {
	name      string
	contents  []byte
	directory bool
	store     bool
	mode      fs.FileMode
	modified  time.Time
}

var (
	errAcceptanceOpen    = errors.New("acceptance source open failure")
	errAcceptancePublish = errors.New("acceptance publication failure")
)

type failingZIPCreationFS struct {
	localfs.LocalFileSystem
	openErr           error
	publishNewErr     error
	publishReplaceErr error
}

func (f failingZIPCreationFS) OpenCopyFile(ctx context.Context, name string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.LocalFileSystem.OpenCopyFile(ctx, name)
}

func (f failingZIPCreationFS) PublishNew(ctx context.Context, temporary, destination string) error {
	if f.publishNewErr != nil {
		return f.publishNewErr
	}
	return f.LocalFileSystem.PublishNew(ctx, temporary, destination)
}

func (f failingZIPCreationFS) PublishReplace(ctx context.Context, temporary, destination string, expected core.FileFingerprint) error {
	if f.publishReplaceErr != nil {
		return f.publishReplaceErr
	}
	return f.LocalFileSystem.PublishReplace(ctx, temporary, destination, expected)
}

type boundedZIPProbeInput struct {
	size       int64
	maximum    int
	entryCount uint16
	reads      int
	largest    int
}

func (b *boundedZIPProbeInput) ReadAt(buffer []byte, offset int64) (int, error) {
	b.reads++
	if len(buffer) > b.largest {
		b.largest = len(buffer)
	}
	if len(buffer) > b.maximum {
		return 0, errors.New("unbounded probe read")
	}
	clear(buffer)
	if offset == 0 && len(buffer) >= 4 {
		copy(buffer, []byte{'P', 'K', 3, 4})
	}
	if offset+int64(len(buffer)) == b.size && len(buffer) >= 22 {
		end := buffer[len(buffer)-22:]
		copy(end, []byte{'P', 'K', 5, 6})
		end[10], end[11] = byte(b.entryCount), byte(b.entryCount>>8)
	}
	return len(buffer), nil
}

func (b *boundedZIPProbeInput) Size() int64  { return b.size }
func (b *boundedZIPProbeInput) Close() error { return nil }

func makeZIP(t *testing.T, fixtures []zipFixture) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := archivezip.NewWriter(&buffer)
	for _, fixture := range fixtures {
		header := &archivezip.FileHeader{Name: fixture.name, Method: archivezip.Deflate}
		if fixture.store || fixture.directory {
			header.Method = archivezip.Store
		}
		mode := fixture.mode
		if fixture.directory {
			mode = os.ModeDir | 0o755
		}
		if mode == 0 {
			mode = 0o600
		}
		header.SetMode(mode)
		if !fixture.modified.IsZero() {
			header.SetModTime(fixture.modified)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(fixture.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func openZIPBytes(t *testing.T, data []byte) (ArchiveSession, *fakeInput) {
	t.Helper()
	input := &fakeInput{data: slices.Clone(data)}
	session, err := NewZIPSource(InputOpenerFunc(func(context.Context, string) (Input, error) { return input, nil })).Open(context.Background(), "archive.zip")
	if err != nil {
		t.Fatal(err)
	}
	return session, input
}

func openZIPBytesError(data []byte) (ArchiveSession, error) {
	input := &fakeInput{data: slices.Clone(data)}
	return NewZIPSource(InputOpenerFunc(func(context.Context, string) (Input, error) { return input, nil })).Open(context.Background(), "archive.zip")
}

func verifyExternalZIP(t *testing.T, archivePath string, expected []byte) {
	t.Helper()
	session, err := NewZIPSource(FileInputOpener{}).Open(context.Background(), archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	root := t.TempDir()
	destination, err := NewLocalExtractionDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if _, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(root, "файл с пробелами.txt"))
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("contents=%q error=%v", actual, err)
	}
	info, err := os.Stat(filepath.Join(root, "empty"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty directory=%v error=%v", info, err)
	}
}

func patchZIPFlags(data []byte, flags uint16) []byte {
	result := slices.Clone(data)
	for offset := 0; offset+10 <= len(result); offset++ {
		switch string(result[offset : offset+4]) {
		case "PK\x03\x04":
			result[offset+6], result[offset+7] = byte(flags), byte(flags>>8)
		case "PK\x01\x02":
			result[offset+8], result[offset+9] = byte(flags), byte(flags>>8)
		}
	}
	return result
}

func patchZIPMethod(data []byte, method uint16) []byte {
	result := slices.Clone(data)
	for offset := 0; offset+12 <= len(result); offset++ {
		switch string(result[offset : offset+4]) {
		case "PK\x03\x04":
			result[offset+8], result[offset+9] = byte(method), byte(method>>8)
		case "PK\x01\x02":
			result[offset+10], result[offset+11] = byte(method), byte(method>>8)
		}
	}
	return result
}

func decodeBase64(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertNoCopyTemporaries(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".marten-copy-") {
			t.Fatalf("temporary file remains: %s", entry.Name())
		}
	}
}

func assertZIPFileContents(t *testing.T, name, expected string) {
	t.Helper()
	contents, err := os.ReadFile(name)
	if err != nil || string(contents) != expected {
		t.Fatalf("contents of %q=%q error=%v; want %q", name, contents, err, expected)
	}
}

func assertNoExtractionTemporaries(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasPrefix(entry.Name(), ".marten-extract-") {
			t.Fatalf("temporary extraction file remains: %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

const zip64Fixture = "UEsDBBQAAAAIADB0CkF+5/9pJAAAACQAAAAGAAAAUkVBRE1FC8nILFYozk3MyVFIy8xJVQDyMvMUojwDzEwU0vKLchNL9LgAUEsBAi0DLQAAAAgAMHQKQX7n/2n//////////wYAFAAAAAAAAAAAAKSBAAAAAFJFQURNRQEAEAAkAAAAAAAAACQAAAAAAAAAUEsGBiwAAAAAAAAALQAtAAAAAAAAAAAAAQAAAAAAAAABAAAAAAAAAEgAAAAAAAAASAAAAAAAAABQSwYHAAAAAJAAAAAAAAAAAQAAAFBLBQYAAAAA////////////////AAA="
