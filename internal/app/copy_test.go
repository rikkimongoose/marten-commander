package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

func startCopy(t *testing.T, filesystem copyFilesystem, sources []string, destination string) (*operations.Manager, operations.Operation) {
	t.Helper()
	manager := operations.NewManager()
	snapshot := operations.ContextSnapshot{ActivePanel: "left", Sources: append([]string(nil), sources...), SourceDirectory: filepath.Dir(sources[0])}
	operation, err := NewCopyService(filesystem, manager).Start(context.Background(), snapshot, sources, destination)
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	return manager, operation
}

func waitCopyState(t *testing.T, manager *operations.Manager, id string, states ...operations.State) operations.Operation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := manager.Get(id)
		if err == nil {
			for _, state := range states {
				if operation.State == state {
					return operation
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	operation, _ := manager.Get(id)
	t.Fatalf("operation did not reach %v: %#v", states, operation)
	return operations.Operation{}
}

func resolveNext(t *testing.T, manager *operations.Manager, id string, action operations.ConflictAction, apply bool) operations.ConflictRequest {
	t.Helper()
	operation := waitCopyState(t, manager, id, operations.WaitingForUser)
	request := *operation.PendingConflict
	_, err := manager.ResolveConflict(operations.ConflictResolution{OperationID: id, ConflictID: request.ConflictID,
		Revision: request.Revision, Action: action, ApplyToAll: apply})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestCopyFilesDirectoriesUnicodeEmptyAndSymlinks(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(source, "Каталог с пробелами", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "пустой каталог"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := []byte{0, 1, 2, 0xff, 0, 4}
	if err := os.WriteFile(filepath.Join(source, "бинарный файл.bin"), binary, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "пустой"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Каталог с пробелами", "nested", "data.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Каталог с пробелами", filepath.Join(source, "ссылка")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("цикл", filepath.Join(source, "цикл")); err != nil {
		t.Fatal(err)
	}

	sources := []string{filepath.Join(source, "бинарный файл.bin"), filepath.Join(source, "пустой"),
		filepath.Join(source, "пустой каталог"), filepath.Join(source, "Каталог с пробелами"), filepath.Join(source, "ссылка"),
		filepath.Join(source, "цикл"), filepath.Join(source, "бинарный файл.bin")}
	manager, operation := startCopy(t, localfs.LocalFileSystem{}, sources, destination)
	defer manager.Close()
	completed := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
	if completed.State != operations.Completed {
		t.Fatalf("copy failed: %#v", completed.Error)
	}
	got, err := os.ReadFile(filepath.Join(destination, "бинарный файл.bin"))
	if err != nil || string(got) != string(binary) {
		t.Fatalf("binary mismatch: %x, %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(destination, "пустой")); err != nil || info.Size() != 0 {
		t.Fatalf("empty file: %#v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(destination, "пустой каталог")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory: %#v, %v", info, err)
	}
	got, err = os.ReadFile(filepath.Join(destination, "Каталог с пробелами", "nested", "data.txt"))
	if err != nil || string(got) != "nested" {
		t.Fatalf("nested: %q, %v", got, err)
	}
	for _, name := range []string{"ссылка", "цикл"} {
		info, err := os.Lstat(filepath.Join(destination, name))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s was followed: %#v, %v", name, info, err)
		}
		from, _ := os.Readlink(filepath.Join(source, name))
		to, _ := os.Readlink(filepath.Join(destination, name))
		if from != to {
			t.Fatalf("link target %q != %q", from, to)
		}
	}
	if completed.Progress.TotalFiles == nil || *completed.Progress.TotalFiles != 5 || completed.Progress.ProcessedFiles != 5 ||
		completed.Progress.TotalBytes == nil || *completed.Progress.TotalBytes != uint64(len(binary)+len("nested")) {
		t.Fatalf("progress: %#v", completed.Progress)
	}
}

func TestCopyFileConflictActions(t *testing.T) {
	for _, action := range []operations.ConflictAction{operations.Replace, operations.Skip, operations.KeepBoth} {
		t.Run(string(action), func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			destination := filepath.Join(root, "destination")
			_ = os.Mkdir(source, 0o755)
			_ = os.Mkdir(destination, 0o755)
			from, to := filepath.Join(source, "report.txt"), filepath.Join(destination, "report.txt")
			_ = os.WriteFile(from, []byte("new"), 0o644)
			_ = os.WriteFile(to, []byte("old"), 0o644)
			manager, operation := startCopy(t, localfs.LocalFileSystem{}, []string{from}, destination)
			defer manager.Close()
			request := resolveNext(t, manager, operation.ID, action, false)
			if request.Type != operations.FileFile {
				t.Fatalf("conflict: %#v", request)
			}
			completed := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
			if completed.State != operations.Completed {
				t.Fatalf("operation: %#v", completed)
			}
			content, _ := os.ReadFile(to)
			switch action {
			case operations.Replace:
				if string(content) != "new" {
					t.Fatalf("replace: %q", content)
				}
			case operations.Skip:
				if string(content) != "old" {
					t.Fatalf("skip: %q", content)
				}
			case operations.KeepBoth:
				copyContent, err := os.ReadFile(filepath.Join(destination, "report (1).txt"))
				if string(content) != "old" || err != nil || string(copyContent) != "new" {
					t.Fatalf("keep both: %q %q %v", content, copyContent, err)
				}
			}
		})
	}
}

func TestCopyMergeAndApplyToAll(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source", "folder")
	destination := filepath.Join(root, "destination")
	_ = os.MkdirAll(filepath.Join(source, "nested"), 0o755)
	_ = os.MkdirAll(filepath.Join(destination, "folder", "nested"), 0o755)
	for _, name := range []string{"one.txt", "two.txt"} {
		_ = os.WriteFile(filepath.Join(source, "nested", name), []byte("new-"+name), 0o644)
		_ = os.WriteFile(filepath.Join(destination, "folder", "nested", name), []byte("old-"+name), 0o644)
	}
	_ = os.WriteFile(filepath.Join(destination, "folder", "kept.txt"), []byte("kept"), 0o644)
	manager, operation := startCopy(t, localfs.LocalFileSystem{}, []string{source}, destination)
	defer manager.Close()
	if request := resolveNext(t, manager, operation.ID, operations.Merge, false); request.Type != operations.DirectoryDirectory {
		t.Fatal(request.Type)
	}
	if request := resolveNext(t, manager, operation.ID, operations.Merge, false); request.Type != operations.DirectoryDirectory {
		t.Fatal(request.Type)
	}
	resolveNext(t, manager, operation.ID, operations.Replace, true)
	completed := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
	if completed.State != operations.Completed {
		t.Fatalf("operation: %#v", completed.Error)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		content, _ := os.ReadFile(filepath.Join(destination, "folder", "nested", name))
		if string(content) != "new-"+name {
			t.Fatalf("apply all did not replace %s: %q", name, content)
		}
	}
	if content, _ := os.ReadFile(filepath.Join(destination, "folder", "kept.txt")); string(content) != "kept" {
		t.Fatalf("merge removed destination data")
	}
}

func TestCopyCancelWhileWaitingForConflict(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(source, 0o755)
	_ = os.Mkdir(destination, 0o755)
	from, to := filepath.Join(source, "same"), filepath.Join(destination, "same")
	_ = os.WriteFile(from, []byte("new"), 0o644)
	_ = os.WriteFile(to, []byte("old"), 0o644)
	manager, operation := startCopy(t, localfs.LocalFileSystem{}, []string{from}, destination)
	defer manager.Close()
	waitCopyState(t, manager, operation.ID, operations.WaitingForUser)
	if _, err := manager.Cancel(operation.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitCopyState(t, manager, operation.ID, operations.Cancelled)
	if content, _ := os.ReadFile(to); string(content) != "old" {
		t.Fatalf("cancel overwrote destination: %q", content)
	}
	if cancelled.PendingConflict != nil {
		t.Fatal("cancel retained conflict")
	}
}

type slowReadCloser struct {
	context context.Context
	io.ReadCloser
	started chan<- struct{}
	once    sync.Once
}

func (r *slowReadCloser) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.context.Done()
	return 0, r.context.Err()
}

type slowCopyFS struct {
	localfs.LocalFileSystem
	started  chan struct{}
	slowPath string
}

func (s *slowCopyFS) OpenCopyFile(ctx context.Context, path string) (io.ReadCloser, error) {
	reader, err := s.LocalFileSystem.OpenCopyFile(ctx, path)
	if err != nil {
		return nil, err
	}
	if path != s.slowPath {
		return reader, nil
	}
	return &slowReadCloser{context: ctx, ReadCloser: reader, started: s.started}, nil
}

func TestCopyCancelMidFileRemovesTemporaryFile(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(source, 0o755)
	_ = os.Mkdir(destination, 0o755)
	first, from := filepath.Join(source, "first.bin"), filepath.Join(source, "large.bin")
	_ = os.WriteFile(first, []byte("complete"), 0o644)
	_ = os.WriteFile(from, make([]byte, 2*copyBufferSize), 0o644)
	filesystem := &slowCopyFS{started: make(chan struct{})}
	// Slow only the large file so the cancelled snapshot proves that already
	// published entries remain visible as a partial result.
	filesystem.slowPath = from
	manager, operation := startCopy(t, filesystem, []string{first, from}, destination)
	defer manager.Close()
	select {
	case <-filesystem.started:
	case <-time.After(time.Second):
		t.Fatal("copy did not start")
	}
	if _, err := manager.Cancel(operation.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitCopyState(t, manager, operation.ID, operations.Cancelled)
	if cancelled.Progress.ProcessedFiles != 1 {
		t.Fatalf("partial progress: %#v", cancelled.Progress)
	}
	deadline := time.Now().Add(time.Second)
	for {
		entries, _ := os.ReadDir(destination)
		if len(entries) == 1 && entries[0].Name() == "first.bin" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("temporary result remained: %#v", entries)
		}
		time.Sleep(time.Millisecond)
	}
}

type racingCopyFS struct {
	localfs.LocalFileSystem
	destination string
	once        sync.Once
}

func (r *racingCopyFS) PublishReplace(ctx context.Context, temporary, destination string, expected core.FileFingerprint) error {
	r.once.Do(func() { _ = os.WriteFile(r.destination, []byte("concurrent"), 0o644) })
	return r.LocalFileSystem.PublishReplace(ctx, temporary, destination, expected)
}

func TestCopyRejectsDestinationChangeBeforeReplacePublication(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(source, 0o755)
	_ = os.Mkdir(destination, 0o755)
	from, to := filepath.Join(source, "same"), filepath.Join(destination, "same")
	_ = os.WriteFile(from, []byte("new"), 0o644)
	_ = os.WriteFile(to, []byte("old"), 0o644)
	filesystem := &racingCopyFS{destination: to}
	manager, operation := startCopy(t, filesystem, []string{from}, destination)
	defer manager.Close()
	resolveNext(t, manager, operation.ID, operations.Replace, false)
	failed := waitCopyState(t, manager, operation.ID, operations.Failed)
	if failed.Error == nil || !strings.Contains(failed.Error.Message, "изменилось") {
		t.Fatalf("failure: %#v", failed.Error)
	}
	if content, _ := os.ReadFile(to); string(content) != "concurrent" {
		t.Fatalf("concurrent destination lost: %q", content)
	}
	entries, _ := os.ReadDir(destination)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".marten-copy-") {
			t.Fatalf("temporary file remained: %s", entry.Name())
		}
	}
}

func TestCopyRejectsSelfCopyAndUnavailableDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	inside := filepath.Join(source, "inside")
	_ = os.MkdirAll(inside, 0o755)
	_ = os.WriteFile(filepath.Join(source, "file"), []byte("x"), 0o644)
	manager, operation := startCopy(t, localfs.LocalFileSystem{}, []string{source}, inside)
	failed := waitCopyState(t, manager, operation.ID, operations.Failed)
	manager.Close()
	if failed.Error == nil || !strings.Contains(failed.Error.Message, "внутрь самого себя") {
		t.Fatalf("self-copy: %#v", failed.Error)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(inside, alias); err != nil {
		t.Fatal(err)
	}
	manager, operation = startCopy(t, localfs.LocalFileSystem{}, []string{source}, alias)
	failed = waitCopyState(t, manager, operation.ID, operations.Failed)
	manager.Close()
	if failed.Error == nil || !strings.Contains(failed.Error.Message, "внутрь самого себя") {
		t.Fatalf("symlink alias self-copy: %#v", failed.Error)
	}
	manager, operation = startCopy(t, localfs.LocalFileSystem{}, []string{filepath.Join(source, "file")}, source)
	failed = waitCopyState(t, manager, operation.ID, operations.Failed)
	manager.Close()
	if failed.Error == nil || !strings.Contains(failed.Error.Message, "совпадают") {
		t.Fatalf("same-file copy: %#v", failed.Error)
	}
	missing := filepath.Join(root, "missing")
	missingManager := operations.NewManager()
	defer missingManager.Close()
	service := NewCopyService(localfs.LocalFileSystem{}, missingManager)
	if _, err := service.Start(context.Background(), operations.ContextSnapshot{}, []string{filepath.Join(source, "file")}, missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing destination: %v", err)
	}
}

type disappearingFS struct {
	localfs.LocalFileSystem
	target string
	mu     sync.Mutex
	calls  int
}

func (f *disappearingFS) Lstat(ctx context.Context, path string) (core.FileEntry, error) {
	if path == f.target {
		f.mu.Lock()
		f.calls++
		call := f.calls
		f.mu.Unlock()
		if call >= 3 {
			return core.FileEntry{}, os.ErrNotExist
		}
	}
	return f.LocalFileSystem.Lstat(ctx, path)
}

func TestCopyReportsPartialResultWhenLaterSourceDisappears(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(source, 0o755)
	_ = os.Mkdir(destination, 0o755)
	first, second := filepath.Join(source, "first"), filepath.Join(source, "second")
	_ = os.WriteFile(first, []byte("first"), 0o644)
	_ = os.WriteFile(second, []byte("second"), 0o644)
	manager, operation := startCopy(t, &disappearingFS{target: second}, []string{first, second}, destination)
	defer manager.Close()
	failed := waitCopyState(t, manager, operation.ID, operations.Failed)
	if failed.Error == nil || failed.Error.Code != "copy_partial" {
		t.Fatalf("partial result: %#v", failed.Error)
	}
	if content, _ := os.ReadFile(filepath.Join(destination, "first")); string(content) != "first" {
		t.Fatalf("first file was not retained")
	}
}

type deniedCopyFS struct{ localfs.LocalFileSystem }

func (deniedCopyFS) CreateTemporaryFile(context.Context, string, fs.FileMode) (core.TemporaryFile, error) {
	return nil, fs.ErrPermission
}

type noSpaceCopyFS struct {
	localfs.LocalFileSystem
	mu    sync.Mutex
	calls int
}

func (f *noSpaceCopyFS) CreateTemporaryFile(ctx context.Context, directory string, mode fs.FileMode) (core.TemporaryFile, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if call > 1 {
		return nil, syscall.ENOSPC
	}
	return f.LocalFileSystem.CreateTemporaryFile(ctx, directory, mode)
}

type specialSourceFS struct {
	localfs.LocalFileSystem
	target string
}

func (f specialSourceFS) Lstat(ctx context.Context, path string) (core.FileEntry, error) {
	if path == f.target {
		return core.FileEntry{Path: path, Name: filepath.Base(path), Kind: core.KindOther}, nil
	}
	return f.LocalFileSystem.Lstat(ctx, path)
}

func TestCopyReportsPermissionNoSpaceAndSpecialObjects(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(source, 0o755)
	_ = os.Mkdir(destination, 0o755)
	from := filepath.Join(source, "file")
	_ = os.WriteFile(from, []byte("data"), 0o644)
	deniedManager := operations.NewManager()
	defer deniedManager.Close()
	if _, err := NewCopyService(deniedCopyFS{}, deniedManager).Start(context.Background(), operations.ContextSnapshot{}, []string{from}, destination); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission error: %v", err)
	}
	manager, operation := startCopy(t, &noSpaceCopyFS{}, []string{from}, destination)
	failed := waitCopyState(t, manager, operation.ID, operations.Failed)
	manager.Close()
	if failed.Error == nil || !strings.Contains(strings.ToLower(failed.Error.Message), "space") {
		t.Fatalf("no-space error: %#v", failed.Error)
	}
	manager, operation = startCopy(t, specialSourceFS{target: from}, []string{from}, destination)
	failed = waitCopyState(t, manager, operation.ID, operations.Failed)
	manager.Close()
	if failed.Error == nil || !strings.Contains(failed.Error.Message, "специальный объект") {
		t.Fatalf("special object: %#v", failed.Error)
	}
}
