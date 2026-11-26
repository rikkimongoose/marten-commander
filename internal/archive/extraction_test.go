package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/operations"
)

func TestValidateExtractionPath(t *testing.T) {
	t.Parallel()
	valid := []string{"file.txt", "dir/sub/file", "Каталог/файл с пробелами.txt"}
	for _, name := range valid {
		if got, err := ValidateExtractionPath(name); err != nil || got != name {
			t.Errorf("ValidateExtractionPath(%q) = %q, %v", name, got, err)
		}
	}
	invalid := []string{
		"../escape.txt", "/tmp/escape.txt", `C:\escape.txt`, `\\server\share\escape.txt`,
		"dir/../../escape.txt", "dir/../escape.txt", "dir//file", "./file", "dir/./file",
		"C:/escape.txt", "file:stream", "bad\x00name", string([]byte{0xff, 'x'}), "",
	}
	for _, name := range invalid {
		if _, err := ValidateExtractionPath(name); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("ValidateExtractionPath(%q) error = %v; want ErrUnsafePath", name, err)
		}
	}
}

func TestExtractorUnicodeSpacesNestedAndUnknownSizes(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		entries:  []ArchiveEntry{{Path: "Каталог/файл с пробелами.txt", Kind: EntryFile}},
		contents: map[string][]byte{"/Каталог/файл с пробелами.txt": []byte("данные")},
	}
	session, _ := openFake(t, backend)
	defer session.Close()
	destination := newFakeDestination()
	report, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(destination.files["Каталог/файл с пробелами.txt"]); got != "данные" {
		t.Fatalf("extracted contents = %q", got)
	}
	if report.BytesWritten != uint64(len([]byte("данные"))) || destination.active != 0 {
		t.Fatalf("report=%+v active temporaries=%d", report, destination.active)
	}
}

func TestExtractorAcceptsZIPStyleDirectoryMarker(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		entries:  []ArchiveEntry{{Path: "directory/", Kind: EntryDirectory}, {Path: "directory/file", Kind: EntryFile}},
		contents: map[string][]byte{"/directory/file": []byte("data")},
	}
	session, _ := openFake(t, backend)
	defer session.Close()
	destination := newFakeDestination()
	if _, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil); err != nil {
		t.Fatal(err)
	}
	if !destination.directories["directory"] || string(destination.files["directory/file"]) != "data" {
		t.Fatalf("directories=%v files=%v", destination.directories, destination.files)
	}
}

func TestExtractorRejectsAbsoluteRawArchivePath(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{entries: []ArchiveEntry{{Path: "/tmp/escape.txt", Kind: EntryFile}}}
	session, _ := openFake(t, backend)
	defer session.Close()
	report, err := NewDefaultExtractor().Extract(context.Background(), session, newFakeDestination(), nil)
	if !errors.Is(err, ErrUnsafePath) || report.FailedPath != "/tmp/escape.txt" || report.FailedStage != StagePlan {
		t.Fatalf("report=%+v error=%v", report, err)
	}
}

func TestExtractorRejectsSessionEntryWithoutRawPath(t *testing.T) {
	t.Parallel()
	session := &directSession{entries: []ArchiveEntry{{Path: "/absolute", Kind: EntryFile}},
		backend: &pathBackend{readers: map[string]io.Reader{"/absolute": strings.NewReader("data")}}}
	report, err := NewDefaultExtractor().Extract(context.Background(), session, newFakeDestination(), nil)
	if !errors.Is(err, ErrInvalidMetadata) || report.FailedPath != "/absolute" || len(report.Extracted) != 0 {
		t.Fatalf("report=%+v error=%v", report, err)
	}
}

func TestExtractorRejectsArchiveLinksAndSpecialEntriesBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, kind := range []EntryKind{EntrySymlink, EntryHardlink, EntrySpecial} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			backend := &fakeBackend{entries: []ArchiveEntry{
				{Path: "before.txt", Kind: EntryFile},
				{Path: "unsupported", Kind: kind, LinkTarget: "outside"},
			}}
			session, _ := openFake(t, backend)
			defer session.Close()
			destination := newFakeDestination()
			report, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
			if !errors.Is(err, ErrUnsupportedEntry) || len(report.Extracted) != 0 || len(destination.files) != 0 {
				t.Fatalf("report=%+v files=%v error=%v", report, destination.files, err)
			}
		})
	}
}

func TestExtractionLimitsFromMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		limits  ExtractionLimits
		entries []ArchiveEntry
	}{
		{"file size", limits(10, 4, 100, 10, 100), []ArchiveEntry{{Path: "file", Kind: EntryFile, Size: int64Pointer(5)}}},
		{"total size", limits(10, 10, 5, 10, 100), []ArchiveEntry{{Path: "a", Kind: EntryFile, Size: int64Pointer(3)}, {Path: "b", Kind: EntryFile, Size: int64Pointer(3)}}},
		{"entry count", limits(1, 10, 100, 10, 100), []ArchiveEntry{{Path: "a", Kind: EntryFile}, {Path: "b", Kind: EntryFile}}},
		{"implicit directories count", limits(2, 10, 100, 10, 100), []ArchiveEntry{{Path: "a/b/file", Kind: EntryFile}}},
		{"depth", limits(10, 10, 100, 2, 100), []ArchiveEntry{{Path: "a/b/c", Kind: EntryFile}}},
		{"ratio", limits(10, 100, 100, 10, 3), []ArchiveEntry{{Path: "file", Kind: EntryFile, Size: int64Pointer(10), CompressedSize: int64Pointer(3)}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			session, _ := openFake(t, &fakeBackend{entries: test.entries})
			defer session.Close()
			extractor, err := NewExtractor(test.limits)
			if err != nil {
				t.Fatal(err)
			}
			destination := newFakeDestination()
			report, err := extractor.Extract(context.Background(), session, destination, nil)
			if !errors.Is(err, ErrLimitExceeded) || len(report.Extracted) != 0 || destination.active != 0 {
				t.Fatalf("report=%+v active=%d error=%v", report, destination.active, err)
			}
		})
	}
}

func TestExtractionLimitsUseActualStreamedBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		limits     ExtractionLimits
		compressed *int64
	}{
		{"file", limits(10, 4, 100, 10, 100), nil},
		{"total", limits(10, 100, 4, 10, 100), nil},
		{"ratio", limits(10, 100, 100, 10, 2), int64Pointer(2)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := &fakeBackend{
				entries:  []ArchiveEntry{{Path: "file", Kind: EntryFile, CompressedSize: test.compressed}},
				contents: map[string][]byte{"/file": []byte("12345")},
			}
			session, _ := openFake(t, backend)
			defer session.Close()
			extractor, _ := NewExtractor(test.limits)
			destination := newFakeDestination()
			report, err := extractor.Extract(context.Background(), session, destination, nil)
			if !errors.Is(err, ErrLimitExceeded) || len(destination.files) != 0 || destination.active != 0 || report.BytesWritten != 0 {
				t.Fatalf("report=%+v files=%v active=%d error=%v", report, destination.files, destination.active, err)
			}
		})
	}
}

func TestExtractorRejectsInvalidMetadataButAllowsUnknownSizes(t *testing.T) {
	t.Parallel()
	for _, entry := range []ArchiveEntry{
		{Path: "negative-size", Kind: EntryFile, Size: int64Pointer(-1)},
		{Path: "negative-compressed", Kind: EntryFile, CompressedSize: int64Pointer(-1)},
	} {
		session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{entry}})
		_, err := NewDefaultExtractor().Extract(context.Background(), session, newFakeDestination(), nil)
		_ = session.Close()
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("entry %+v error = %v", entry, err)
		}
	}

	session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: "unknown", Kind: EntryFile}}, contents: map[string][]byte{"/unknown": []byte("ok")}})
	defer session.Close()
	if _, err := NewDefaultExtractor().Extract(context.Background(), session, newFakeDestination(), nil); err != nil {
		t.Fatalf("unknown sizes rejected: %v", err)
	}
}

func TestExtractorConflictSkipReplaceAndFailClosed(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}}, contents: map[string][]byte{"/file": []byte("new")}}

	t.Run("no resolver", func(t *testing.T) {
		session, _ := openFake(t, backend)
		defer session.Close()
		destination := newFakeDestination()
		destination.files["file"] = []byte("old")
		_, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
		if !errors.Is(err, ErrConflict) || string(destination.files["file"]) != "old" {
			t.Fatalf("file=%q error=%v", destination.files["file"], err)
		}
	})

	t.Run("skip", func(t *testing.T) {
		session, _ := openFake(t, backend)
		defer session.Close()
		destination := newFakeDestination()
		destination.files["file"] = []byte("old")
		report, err := NewDefaultExtractor().Extract(context.Background(), session, destination,
			ExtractionConflictResolverFunc(func(context.Context, ExtractionConflict) (operations.ConflictAction, error) {
				return operations.Skip, nil
			}))
		if err != nil || len(report.Skipped) != 1 || string(destination.files["file"]) != "old" {
			t.Fatalf("report=%+v file=%q error=%v", report, destination.files["file"], err)
		}
	})

	t.Run("replace", func(t *testing.T) {
		session, _ := openFake(t, backend)
		defer session.Close()
		destination := newFakeDestination()
		destination.files["file"] = []byte("old")
		_, err := NewDefaultExtractor().Extract(context.Background(), session, destination,
			ExtractionConflictResolverFunc(func(context.Context, ExtractionConflict) (operations.ConflictAction, error) {
				return operations.Replace, nil
			}))
		if err != nil || string(destination.files["file"]) != "new" {
			t.Fatalf("file=%q error=%v", destination.files["file"], err)
		}
	})
}

func TestExistingFileSurvivesFailedReplacementStream(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}},
		streamReader: &prefixErrorReader{prefix: []byte("partial replacement"), err: errors.New("damaged archive")}}
	session, _ := openFake(t, backend)
	defer session.Close()
	destination := newFakeDestination()
	destination.files["file"] = []byte("original")
	_, err := NewDefaultExtractor().Extract(context.Background(), session, destination,
		ExtractionConflictResolverFunc(func(context.Context, ExtractionConflict) (operations.ConflictAction, error) {
			return operations.Replace, nil
		}))
	if !errors.Is(err, ErrRead) || string(destination.files["file"]) != "original" || destination.active != 0 {
		t.Fatalf("file=%q active=%d error=%v", destination.files["file"], destination.active, err)
	}
}

func TestExtractionCounterOverflowIsRejected(t *testing.T) {
	t.Parallel()
	if _, err := addChecked(^uint64(0)-2, 3, ^uint64(0)); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("overflow error = %v", err)
	}
	compressed := int64Pointer(1<<62 + 1)
	if err := checkRatio(^uint64(0), compressed, 1000); err != nil {
		t.Fatalf("overflow-safe ratio rejected valid bounds: %v", err)
	}
}

func TestExtractorCancellationReadAndWriteErrorsCleanTemporaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		reader    io.Reader
		configure func(*fakeDestination)
		cancel    bool
		want      error
	}{
		{"read", &prefixErrorReader{prefix: []byte("part"), err: errors.New("damaged")}, nil, false, ErrRead},
		{"read with final data", &terminalErrorReader{data: []byte("part"), err: errors.New("checksum mismatch")}, nil, false, ErrRead},
		{"write", strings.NewReader("contents"), func(d *fakeDestination) { d.writeErrorAfter = 2 }, false, errFakeWrite},
		{"cancel", strings.NewReader(strings.Repeat("x", extractionBufferSize*2)), func(d *fakeDestination) { d.cancelOnWrite = true }, true, context.Canceled},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			backend := &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}}, streamReader: test.reader}
			session, _ := openFake(t, backend)
			defer session.Close()
			destination := newFakeDestination()
			destination.cancel = cancel
			if test.configure != nil {
				test.configure(destination)
			}
			_, err := NewDefaultExtractor().Extract(ctx, session, destination, nil)
			if !errors.Is(err, test.want) || len(destination.files) != 0 || destination.active != 0 {
				t.Fatalf("files=%v active=%d error=%v", destination.files, destination.active, err)
			}
		})
	}
}

func TestExtractorDestinationIOErrorsCleanTemporaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		configure func(*fakeDestination)
		want      error
	}{
		{"create", func(d *fakeDestination) { d.createError = errFakeCreate }, errFakeCreate},
		{"sync", func(d *fakeDestination) { d.syncError = errFakeSync }, errFakeSync},
		{"close", func(d *fakeDestination) { d.closeError = errFakeClose }, errFakeClose},
		{"publish", func(d *fakeDestination) { d.publishError = errFakePublish }, errFakePublish},
		{"cleanup preserves primary", func(d *fakeDestination) {
			d.writeErrorAfter = 2
			d.discardError = errFakeDiscard
		}, errFakeWrite},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}},
				contents: map[string][]byte{"/file": []byte("contents")}})
			defer session.Close()
			destination := newFakeDestination()
			test.configure(destination)
			_, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
			if !errors.Is(err, test.want) || len(destination.files) != 0 || destination.active != 0 {
				t.Fatalf("files=%v active=%d error=%v", destination.files, destination.active, err)
			}
			if test.name == "cleanup preserves primary" && !errors.Is(err, errFakeDiscard) {
				t.Fatalf("cleanup cause lost: %v", err)
			}
		})
	}
}

func TestExtractorInputCloseErrorPreventsPublication(t *testing.T) {
	t.Parallel()
	session := &directSession{entries: []ArchiveEntry{{Path: "/file", RawPath: "file", Kind: EntryFile}},
		backend: &pathBackend{readers: map[string]io.Reader{"/file": strings.NewReader("contents")}, closeErr: errFakeInputClose}}
	destination := newFakeDestination()
	_, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
	if !errors.Is(err, errFakeInputClose) || len(destination.files) != 0 || destination.active != 0 {
		t.Fatalf("files=%v active=%d error=%v", destination.files, destination.active, err)
	}
}

func TestExtractorClosesArchiveStreamOnDestinationFailure(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}},
		contents: map[string][]byte{"/file": []byte("contents")}}
	session, _ := openFake(t, backend)
	defer session.Close()
	destination := newFakeDestination()
	destination.writeErrorAfter = 1
	if _, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil); !errors.Is(err, errFakeWrite) {
		t.Fatalf("Extract error = %v", err)
	}
	if backend.streamCloseCount() != 1 || destination.active != 0 {
		t.Fatalf("closed streams=%d active temporaries=%d", backend.streamCloseCount(), destination.active)
	}
}

func TestExtractorPartialReportCountsOnlyPublishedFiles(t *testing.T) {
	t.Parallel()
	backend := &pathBackend{
		entries: []ArchiveEntry{{Path: "/a", RawPath: "a", Kind: EntryFile}, {Path: "/b", RawPath: "b", Kind: EntryFile}},
		readers: map[string]io.Reader{"/a": strings.NewReader("first"), "/b": &prefixErrorReader{prefix: []byte("bad"), err: errors.New("broken")}},
	}
	session := &directSession{backend: backend, entries: backend.entries}
	destination := newFakeDestination()
	report, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
	if err == nil || !errors.Is(err, ErrRead) {
		t.Fatalf("error = %v", err)
	}
	if strings.Join(report.Extracted, ",") != "a" || report.BytesWritten != 5 || string(destination.files["a"]) != "first" || destination.active != 0 {
		t.Fatalf("report=%+v files=%v active=%d", report, destination.files, destination.active)
	}
}

func TestActualTotalLimitKeepsPreviouslyPublishedFiles(t *testing.T) {
	t.Parallel()
	entries := []ArchiveEntry{{Path: "/a", RawPath: "a", Kind: EntryFile}, {Path: "/b", RawPath: "b", Kind: EntryFile}}
	session := &directSession{entries: entries, backend: &pathBackend{readers: map[string]io.Reader{
		"/a": strings.NewReader("123"), "/b": strings.NewReader("456"),
	}}}
	extractor, _ := NewExtractor(limits(10, 10, 5, 10, 100))
	destination := newFakeDestination()
	report, err := extractor.Extract(context.Background(), session, destination, nil)
	if !errors.Is(err, ErrLimitExceeded) || strings.Join(report.Extracted, ",") != "a" || report.BytesWritten != 3 ||
		string(destination.files["a"]) != "123" || destination.files["b"] != nil || destination.active != 0 {
		t.Fatalf("report=%+v files=%v active=%d error=%v", report, destination.files, destination.active, err)
	}
}

func TestCancellationBetweenEntriesReturnsPartialReport(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	entries := []ArchiveEntry{{Path: "/a", RawPath: "a", Kind: EntryFile}, {Path: "/b", RawPath: "b", Kind: EntryFile}}
	session := &directSession{entries: entries, backend: &pathBackend{readers: map[string]io.Reader{
		"/a": strings.NewReader("first"), "/b": strings.NewReader("second"),
	}}}
	destination := newFakeDestination()
	destination.cancel = cancel
	destination.cancelAfterPublish = true
	report, err := NewDefaultExtractor().Extract(ctx, session, destination, nil)
	if !errors.Is(err, context.Canceled) || strings.Join(report.Extracted, ",") != "a" ||
		string(destination.files["a"]) != "first" || destination.files["b"] != nil || destination.active != 0 {
		t.Fatalf("report=%+v files=%v active=%d error=%v", report, destination.files, destination.active, err)
	}
}

func TestExtractorStreamsLargeFileWithBoundedBuffer(t *testing.T) {
	t.Parallel()
	reader := &sizedPatternReader{remaining: 8 << 20}
	session := &directSession{entries: []ArchiveEntry{{Path: "/large", RawPath: "large", Name: "large", Kind: EntryFile}},
		backend: &pathBackend{readers: map[string]io.Reader{"/large": reader}}}
	destination := newFakeDestination()
	destination.countOnly = true
	report, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reader.maxBuffer > extractionBufferSize || report.BytesWritten != 8<<20 || destination.written != 8<<20 {
		t.Fatalf("max read=%d report=%d destination=%d", reader.maxBuffer, report.BytesWritten, destination.written)
	}
}

func TestDefaultExtractionLimits(t *testing.T) {
	t.Parallel()
	limits := DefaultExtractionLimits()
	if limits.MaxEntries != 100_000 || limits.MaxFileSize != 4<<30 || limits.MaxTotalSize != 20<<30 ||
		limits.MaxDepth != 64 || limits.MaxExpansionRatio != 1_000 {
		t.Fatalf("defaults = %+v", limits)
	}
	if _, err := NewExtractor(ExtractionLimits{}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("zero limits error = %v", err)
	}
}

func limits(entries, file, total, depth, ratio uint64) ExtractionLimits {
	return ExtractionLimits{MaxEntries: entries, MaxFileSize: file, MaxTotalSize: total, MaxDepth: depth, MaxExpansionRatio: ratio}
}

func int64Pointer(value int64) *int64 { return &value }

var (
	errFakeWrite      = errors.New("fake disk write failure")
	errFakeCreate     = errors.New("fake temporary creation failure")
	errFakeSync       = errors.New("fake disk sync failure")
	errFakeClose      = errors.New("fake disk close failure")
	errFakePublish    = errors.New("fake publication failure")
	errFakeDiscard    = errors.New("fake cleanup failure")
	errFakeInputClose = errors.New("fake input close failure")
)

type fakeDestination struct {
	mu                 sync.Mutex
	directories        map[string]bool
	files              map[string][]byte
	links              map[string]DestinationKind
	active             int
	written            uint64
	writeErrorAfter    int
	cancelOnWrite      bool
	cancel             context.CancelFunc
	countOnly          bool
	createError        error
	syncError          error
	closeError         error
	publishError       error
	discardError       error
	cancelAfterPublish bool
}

func newFakeDestination() *fakeDestination {
	return &fakeDestination{directories: map[string]bool{"": true}, files: make(map[string][]byte), links: make(map[string]DestinationKind)}
}

func (d *fakeDestination) EnsureDirectory(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if kind := d.links[name]; kind != "" {
		return ErrUnsafeDestination
	}
	if _, exists := d.files[name]; exists {
		return ErrConflict
	}
	d.directories[name] = true
	return nil
}

func (d *fakeDestination) Inspect(_ context.Context, name string) (DestinationEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if kind := d.links[name]; kind != "" {
		return DestinationEntry{Kind: kind, Token: "link-token", Identity: "link-identity"}, nil
	}
	if data, exists := d.files[name]; exists {
		return DestinationEntry{Kind: DestinationFile, Token: fakeToken(data), Identity: fakeToken(data)}, nil
	}
	if d.directories[name] {
		return DestinationEntry{Kind: DestinationDirectory, Token: "directory-token", Identity: "directory-identity"}, nil
	}
	return DestinationEntry{}, ErrEntryNotFound
}

func (d *fakeDestination) CreateTemporary(_ context.Context, name string) (ExtractionFile, error) {
	d.mu.Lock()
	if d.createError != nil {
		err := d.createError
		d.mu.Unlock()
		return nil, err
	}
	d.active++
	d.mu.Unlock()
	return &fakeExtractionFile{destination: d, name: name}, nil
}

type fakeExtractionFile struct {
	destination *fakeDestination
	name        string
	buffer      bytes.Buffer
	closed      bool
	consumed    bool
}

func (f *fakeExtractionFile) Write(data []byte) (int, error) {
	d := f.destination
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancelOnWrite && d.cancel != nil {
		d.cancel()
		d.cancelOnWrite = false
	}
	if d.writeErrorAfter > 0 && f.buffer.Len() >= d.writeErrorAfter {
		return 0, errFakeWrite
	}
	if d.writeErrorAfter > 0 && f.buffer.Len()+len(data) > d.writeErrorAfter {
		allowed := d.writeErrorAfter - f.buffer.Len()
		if allowed > 0 {
			d.written += uint64(allowed)
			_, _ = f.buffer.Write(data[:allowed])
		}
		return allowed, errFakeWrite
	}
	d.written += uint64(len(data))
	if d.countOnly {
		return len(data), nil
	}
	return f.buffer.Write(data)
}
func (f *fakeExtractionFile) Sync() error { return f.destination.syncError }
func (f *fakeExtractionFile) Close() error {
	f.closed = true
	return f.destination.closeError
}
func (f *fakeExtractionFile) PublishNew(context.Context) error {
	d := f.destination
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.publishError != nil {
		return d.publishError
	}
	if _, exists := d.files[f.name]; exists {
		return ErrConflict
	}
	d.files[f.name] = append([]byte(nil), f.buffer.Bytes()...)
	f.consumeLocked()
	if d.cancelAfterPublish && d.cancel != nil {
		d.cancelAfterPublish = false
		d.cancel()
	}
	return nil
}
func (f *fakeExtractionFile) PublishReplace(_ context.Context, token, identity string) error {
	d := f.destination
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.publishError != nil {
		return d.publishError
	}
	current, exists := d.files[f.name]
	if !exists || fakeToken(current) != token || fakeToken(current) != identity {
		return ErrConflict
	}
	d.files[f.name] = append([]byte(nil), f.buffer.Bytes()...)
	f.consumeLocked()
	return nil
}
func (f *fakeExtractionFile) Discard(context.Context) error {
	f.destination.mu.Lock()
	defer f.destination.mu.Unlock()
	f.consumeLocked()
	return f.destination.discardError
}
func (f *fakeExtractionFile) consumeLocked() {
	if !f.consumed {
		f.consumed = true
		f.destination.active--
	}
}
func fakeToken(data []byte) string { return string(data) }

type prefixErrorReader struct {
	prefix []byte
	err    error
}

type terminalErrorReader struct {
	data []byte
	err  error
}

func (r *terminalErrorReader) Read(buffer []byte) (int, error) {
	if r.data == nil {
		return 0, io.EOF
	}
	n := copy(buffer, r.data)
	r.data = nil
	return n, r.err
}

func (r *prefixErrorReader) Read(buffer []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(buffer, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return 0, r.err
}

type sizedPatternReader struct {
	remaining int
	maxBuffer int
}

func (r *sizedPatternReader) Read(buffer []byte) (int, error) {
	if len(buffer) > r.maxBuffer {
		r.maxBuffer = len(buffer)
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(buffer), r.remaining)
	for index := 0; index < n; index++ {
		buffer[index] = byte(index)
	}
	r.remaining -= n
	return n, nil
}

type pathBackend struct {
	entries  []ArchiveEntry
	readers  map[string]io.Reader
	closeErr error
}

type directSession struct {
	backend *pathBackend
	entries []ArchiveEntry
}

func (s *directSession) Format() Format { return "fake" }
func (s *directSession) ReadDir(_ context.Context, name string) ([]ArchiveEntry, error) {
	if name != "/" {
		return nil, ErrEntryNotFound
	}
	return append([]ArchiveEntry(nil), s.entries...), nil
}
func (s *directSession) Stat(context.Context, string) (ArchiveEntry, error) {
	return ArchiveEntry{}, ErrEntryNotFound
}
func (s *directSession) Open(_ context.Context, name string) (io.ReadCloser, error) {
	reader := s.backend.readers[name]
	if reader == nil {
		return nil, ErrEntryNotFound
	}
	return &readerWithCloseError{Reader: reader, err: s.backend.closeErr}, nil
}
func (s *directSession) Close() error { return nil }

type readerWithCloseError struct {
	io.Reader
	err error
}

func (r *readerWithCloseError) Close() error { return r.err }
