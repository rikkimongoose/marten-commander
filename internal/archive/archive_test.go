package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestNormalizePath(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                "/",
		"/":               "/",
		"docs/readme.txt": "/docs/readme.txt",
		"/docs//./readme": "/docs/readme",
		"кириллица/a b":   "/кириллица/a b",
	}
	for input, want := range tests {
		input, want := input, want
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizePath(input)
			if err != nil || got != want {
				t.Fatalf("NormalizePath(%q) = %q, %v; want %q", input, got, err, want)
			}
		})
	}
}

func TestNormalizePathRejectsEscapeAndNonPOSIXPaths(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"..", "../file", "safe/../../file", "/../file", `dir\file`, "bad\x00name"} {
		_, err := NormalizePath(input)
		if !errors.Is(err, ErrUnsafePath) {
			t.Errorf("NormalizePath(%q) error = %v; want ErrUnsafePath", input, err)
		}
		var pathErr *PathError
		if !errors.As(err, &pathErr) || pathErr.Path != input {
			t.Errorf("NormalizePath(%q) path diagnostic = %#v", input, pathErr)
		}
	}
}

func TestSessionBuildsNestedImplicitAndEmptyDirectories(t *testing.T) {
	t.Parallel()
	size := int64(11)
	modified := time.Date(2026, time.November, 26, 12, 30, 0, 0, time.UTC)
	backend := &fakeBackend{entries: []ArchiveEntry{
		{Path: "nested/deep/file.txt", Kind: EntryFile, Size: &size, ModifiedAt: &modified, Extra: map[string]string{"method": "fake"}},
		{Path: "empty", Kind: EntryDirectory},
	}}
	session, input := openFake(t, backend)
	t.Cleanup(func() { _ = session.Close() })

	root, err := session.ReadDir(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryNames(root); !reflect.DeepEqual(got, []string{"empty", "nested"}) {
		t.Fatalf("root entries = %v", got)
	}
	if root[1].Size != nil || root[1].ModifiedAt != nil {
		t.Fatalf("implicit directory inherited metadata: %+v", root[1])
	}
	deep, err := session.ReadDir(context.Background(), "nested/deep")
	if err != nil {
		t.Fatal(err)
	}
	if len(deep) != 1 || deep[0].Path != "/nested/deep/file.txt" || deep[0].Name != "file.txt" ||
		deep[0].Size == nil || *deep[0].Size != size || deep[0].ModifiedAt == nil || !deep[0].ModifiedAt.Equal(modified) {
		t.Fatalf("file metadata = %+v", deep)
	}
	deep[0].Extra["method"] = "mutated"
	stat, err := session.Stat(context.Background(), deep[0].Path)
	if err != nil || stat.Extra["method"] != "fake" {
		t.Fatalf("metadata was not isolated: %+v, %v", stat, err)
	}
	empty, err := session.ReadDir(context.Background(), "/empty")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty directory = %+v, %v", empty, err)
	}
	if input.closeCount() != 0 {
		t.Fatal("input closed while session is alive")
	}
}

func TestEmptyArchive(t *testing.T) {
	t.Parallel()
	session, _ := openFake(t, &fakeBackend{})
	defer session.Close()
	entries, err := session.ReadDir(context.Background(), "")
	if err != nil || len(entries) != 0 {
		t.Fatalf("ReadDir(root) = %+v, %v", entries, err)
	}
	root, err := session.Stat(context.Background(), "/")
	if err != nil || root.Kind != EntryDirectory {
		t.Fatalf("Stat(root) = %+v, %v", root, err)
	}
}

func TestUnsafeBackendPathFailsWithDiagnosticAndClosesResources(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{entries: []ArchiveEntry{{Path: "../../escape", Kind: EntryFile}}}
	input := &fakeInput{}
	source := fakeSource(input, backend, DefiniteMatch)
	_, err := source.Open(context.Background(), "unsafe.fake")
	if !errors.Is(err, ErrUnsafePath) || !errors.Is(err, ErrCorruptArchive) {
		t.Fatalf("Open error = %v", err)
	}
	if input.closeCount() != 1 || backend.closeCount() != 1 {
		t.Fatalf("close counts input=%d backend=%d", input.closeCount(), backend.closeCount())
	}
}

func TestSessionRejectsConflictingAndInvalidEntries(t *testing.T) {
	t.Parallel()
	tests := map[string][]ArchiveEntry{
		"duplicate":        {{Path: "same", Kind: EntryFile}, {Path: "same", Kind: EntryFile}},
		"file parent":      {{Path: "parent", Kind: EntryFile}, {Path: "parent/child", Kind: EntryFile}},
		"name mismatch":    {{Path: "actual", Name: "different", Kind: EntryFile}},
		"unsupported kind": {{Path: "unknown", Kind: "unknown"}},
	}
	for name, entries := range tests {
		name, entries := name, entries
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := fakeSource(&fakeInput{}, &fakeBackend{entries: entries}, DefiniteMatch).Open(context.Background(), "bad")
			if err == nil {
				t.Fatal("Open succeeded")
			}
		})
	}
}

func TestOpenStreamsContentAndSessionOwnsResources(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		entries:  []ArchiveEntry{{Path: "large.bin", Kind: EntryFile}},
		contents: map[string][]byte{"/large.bin": bytes.Repeat([]byte("0123456789"), 10000)},
	}
	session, input := openFake(t, backend)
	stream, err := session.Open(context.Background(), "large.bin")
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 17)
	if n, err := stream.Read(buffer); err != nil || n != len(buffer) {
		t.Fatalf("streamed Read = %d, %v", n, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if input.closeCount() != 1 || backend.closeCount() != 1 || backend.streamCloseCount() != 1 {
		t.Fatalf("close counts input=%d backend=%d stream=%d", input.closeCount(), backend.closeCount(), backend.streamCloseCount())
	}
	if _, err := stream.Read(buffer); !errors.Is(err, ErrClosed) {
		t.Fatalf("Read after session close = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if input.closeCount() != 1 || backend.closeCount() != 1 {
		t.Fatal("second Close released resources again")
	}
}

func TestClosedSessionRejectsOperations(t *testing.T) {
	t.Parallel()
	session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}}})
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.ReadDir(context.Background(), "/"); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadDir error = %v", err)
	}
	if _, err := session.Stat(context.Background(), "/file"); !errors.Is(err, ErrClosed) {
		t.Errorf("Stat error = %v", err)
	}
	if _, err := session.Open(context.Background(), "/file"); !errors.Is(err, ErrClosed) {
		t.Errorf("Open error = %v", err)
	}
}

func TestOperationCancellation(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	openerCalled := false
	source := NewSource(InputOpenerFunc(func(context.Context, string) (Input, error) {
		openerCalled = true
		return &fakeInput{}, nil
	}), NewRegistry())
	if _, err := source.Open(cancelled, "archive"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open canceled error = %v", err)
	}
	if openerCalled {
		t.Fatal("opener called for canceled operation")
	}

	session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}}})
	defer session.Close()
	if _, err := session.Stat(cancelled, "/file"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stat canceled error = %v", err)
	}

	streamCtx, stop := context.WithCancel(context.Background())
	stream, err := session.Open(streamCtx, "/file")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	deadline := time.Now().Add(time.Second)
	for {
		_, err = stream.Read(make([]byte, 1))
		if errors.Is(err, context.Canceled) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream was not canceled: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReadErrorsPreserveCauseAndClassification(t *testing.T) {
	t.Parallel()
	cause := errors.New("broken transport")
	backend := &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}}, openErr: cause}
	session, _ := openFake(t, backend)
	defer session.Close()
	_, err := session.Open(context.Background(), "/file")
	if !errors.Is(err, ErrRead) || !errors.Is(err, cause) {
		t.Fatalf("Open entry error = %v", err)
	}

	listBackend := &fakeBackend{entriesErr: cause}
	_, err = fakeSource(&fakeInput{}, listBackend, DefiniteMatch).Open(context.Background(), "broken")
	if !errors.Is(err, ErrRead) || !errors.Is(err, cause) {
		t.Fatalf("Entries error = %v", err)
	}
}

func TestStreamReadErrorPreservesCauseAndClassification(t *testing.T) {
	t.Parallel()
	cause := errors.New("stream interrupted")
	backend := &fakeBackend{
		entries:      []ArchiveEntry{{Path: "file", Kind: EntryFile}},
		streamReader: &errorReader{err: cause},
	}
	session, _ := openFake(t, backend)
	defer session.Close()
	stream, err := session.Open(context.Background(), "/file")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_, err = stream.Read(make([]byte, 8))
	if !errors.Is(err, ErrRead) || !errors.Is(err, cause) {
		t.Fatalf("Read error = %v", err)
	}
}

func TestUnknownFormatClosesInput(t *testing.T) {
	t.Parallel()
	input := &fakeInput{}
	registry := NewRegistry()
	if err := registry.Register(&fakeDriver{format: "fake", match: NoMatch}); err != nil {
		t.Fatal(err)
	}
	_, err := NewSource(InputOpenerFunc(func(context.Context, string) (Input, error) { return input, nil }), registry).Open(context.Background(), "unknown")
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Open error = %v", err)
	}
	if input.closeCount() != 1 {
		t.Fatalf("input close count = %d", input.closeCount())
	}
}

func TestRegistrySelectionAndDuplicateFormat(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	possible := &fakeDriver{format: "possible", match: PossibleMatch, backend: &fakeBackend{}}
	definite := &fakeDriver{format: "definite", match: DefiniteMatch, backend: &fakeBackend{}}
	if err := registry.Register(possible); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(definite); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&fakeDriver{format: "definite"}); !errors.Is(err, ErrFormatRegistered) {
		t.Fatalf("duplicate Register error = %v", err)
	}
	source := NewSource(InputOpenerFunc(func(context.Context, string) (Input, error) { return &fakeInput{}, nil }), registry)
	session, err := source.Open(context.Background(), "detected")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.Format() != "definite" || definite.openCount != 1 || possible.openCount != 0 {
		t.Fatalf("selection format=%q possible=%d definite=%d", session.Format(), possible.openCount, definite.openCount)
	}
}

func TestEntryLookupErrors(t *testing.T) {
	t.Parallel()
	session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: "directory", Kind: EntryDirectory}, {Path: "file", Kind: EntryFile}}})
	defer session.Close()
	if _, err := session.Stat(context.Background(), "/missing"); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("missing Stat error = %v", err)
	}
	if _, err := session.ReadDir(context.Background(), "/file"); !errors.Is(err, ErrNotDirectory) {
		t.Errorf("file ReadDir error = %v", err)
	}
	if _, err := session.Open(context.Background(), "/directory"); !errors.Is(err, ErrNotFile) {
		t.Errorf("directory Open error = %v", err)
	}
}

func entryNames(entries []ArchiveEntry) []string {
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Name
	}
	return result
}

func openFake(t *testing.T, backend *fakeBackend) (ArchiveSession, *fakeInput) {
	t.Helper()
	input := &fakeInput{data: []byte("FAKE")}
	session, err := fakeSource(input, backend, DefiniteMatch).Open(context.Background(), "archive.fake")
	if err != nil {
		t.Fatal(err)
	}
	return session, input
}

func fakeSource(input *fakeInput, backend *fakeBackend, match Match) *Source {
	registry := NewRegistry()
	if err := registry.Register(&fakeDriver{format: "fake", match: match, backend: backend}); err != nil {
		panic(err)
	}
	return NewSource(InputOpenerFunc(func(context.Context, string) (Input, error) { return input, nil }), registry)
}

type fakeInput struct {
	mu     sync.Mutex
	data   []byte
	closed int
}

func (f *fakeInput) ReadAt(buffer []byte, offset int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed != 0 {
		return 0, ErrClosed
	}
	if offset >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(buffer, f.data[offset:])
	if n < len(buffer) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeInput) Size() int64 { return int64(len(f.data)) }
func (f *fakeInput) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}
func (f *fakeInput) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

type fakeDriver struct {
	format    Format
	match     Match
	probeErr  error
	openErr   error
	backend   *fakeBackend
	openCount int
}

func (f *fakeDriver) Format() Format { return f.format }
func (f *fakeDriver) Probe(context.Context, Input) (Match, error) {
	return f.match, f.probeErr
}
func (f *fakeDriver) Open(context.Context, Input) (BackendSession, error) {
	f.openCount++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.backend, nil
}

type fakeBackend struct {
	mu            sync.Mutex
	entries       []ArchiveEntry
	entriesErr    error
	contents      map[string][]byte
	openErr       error
	streamReader  io.Reader
	closed        int
	streamsClosed int
}

func (f *fakeBackend) Entries(context.Context) ([]ArchiveEntry, error) {
	return append([]ArchiveEntry(nil), f.entries...), f.entriesErr
}
func (f *fakeBackend) Open(_ context.Context, name string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	reader := f.streamReader
	if reader == nil {
		reader = bytes.NewReader(f.contents[name])
	}
	return &trackedReadCloser{Reader: reader, close: func() {
		f.mu.Lock()
		f.streamsClosed++
		f.mu.Unlock()
	}}, nil
}
func (f *fakeBackend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}
func (f *fakeBackend) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}
func (f *fakeBackend) streamCloseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streamsClosed
}

type trackedReadCloser struct {
	io.Reader
	once  sync.Once
	close func()
}

func (r *trackedReadCloser) Close() error {
	r.once.Do(r.close)
	return nil
}

type errorReader struct{ err error }

func (r *errorReader) Read([]byte) (int, error) { return 0, r.err }
