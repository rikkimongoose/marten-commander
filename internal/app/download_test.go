package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type downloadRemote struct {
	entries map[string]core.FileEntry
	data    map[string][]byte
	open    func(context.Context, string) (io.ReadCloser, error)
}

func (r *downloadRemote) Lstat(_ context.Context, connectionID, name string) (core.FileEntry, error) {
	if connectionID != "session-1" {
		return core.FileEntry{}, errors.New("wrong session")
	}
	entry, ok := r.entries[name]
	if !ok {
		return core.FileEntry{}, fs.ErrNotExist
	}
	return entry, nil
}
func (r *downloadRemote) Open(ctx context.Context, connectionID, name string) (io.ReadCloser, error) {
	if connectionID != "session-1" {
		return nil, errors.New("wrong session")
	}
	if r.open != nil {
		return r.open(ctx, name)
	}
	return io.NopCloser(bytes.NewReader(r.data[name])), nil
}

func remoteEntry(name string, kind core.EntryKind, size int64) core.FileEntry {
	return core.FileEntry{Path: "/remote/" + name, Name: name, Kind: kind, Size: size, Mode: 0o640, ModifiedAt: time.Now()}
}

func startDownload(t *testing.T, remote *downloadRemote, local downloadDestination, sources []string, destination string) (*operations.Manager, operations.Operation) {
	t.Helper()
	manager := operations.NewManager()
	snapshot := operations.ContextSnapshot{ActivePanel: "left", Sources: append([]string(nil), sources...), SourceDirectory: "remote"}
	operation, err := NewDownloadService(remote, local, manager).Start(context.Background(), snapshot, "session-1", sources, destination)
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	return manager, operation
}

func TestDownloadSmallLargeUnicodeAndSHA256(t *testing.T) {
	destination := t.TempDir()
	large := make([]byte, 5*1024*1024+137)
	for index := range large {
		large[index] = byte(index*31 + 7)
	}
	files := map[string][]byte{"/remote/маленький файл.txt": []byte("привет SFTP"), "/remote/большой файл.bin": large}
	remote := &downloadRemote{entries: map[string]core.FileEntry{}, data: files}
	for name, data := range files {
		remote.entries[name] = remoteEntry(filepath.Base(name), core.KindFile, int64(len(data)))
	}
	manager, operation := startDownload(t, remote, localfs.LocalFileSystem{}, []string{"/remote/маленький файл.txt", "/remote/большой файл.bin"}, destination)
	defer manager.Close()
	completed := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
	if completed.State != operations.Completed {
		t.Fatalf("download = %#v", completed)
	}
	for source, want := range files {
		got, err := os.ReadFile(filepath.Join(destination, filepath.Base(source)))
		if err != nil || sha256.Sum256(got) != sha256.Sum256(want) {
			t.Fatalf("sha256 %s mismatch: %v", source, err)
		}
	}
	if completed.Progress.ProcessedFiles != 2 || completed.Progress.ProcessedBytes != uint64(len(files["/remote/маленький файл.txt"])+len(large)) {
		t.Fatalf("progress = %#v", completed.Progress)
	}
	if completed.Context.SourceConnectionID != "session-1" || completed.Context.SourceType != "remote" {
		t.Fatalf("context = %#v", completed.Context)
	}
}

func TestDownloadConflictUsesExistingResolver(t *testing.T) {
	destination := t.TempDir()
	target := filepath.Join(destination, "report.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	remote := &downloadRemote{entries: map[string]core.FileEntry{"/remote/report.txt": remoteEntry("report.txt", core.KindFile, 3)},
		data: map[string][]byte{"/remote/report.txt": []byte("new")}}
	manager, operation := startDownload(t, remote, localfs.LocalFileSystem{}, []string{"/remote/report.txt"}, destination)
	defer manager.Close()
	request := resolveNext(t, manager, operation.ID, operations.Replace, false)
	if request.Type != operations.FileFile || request.Source.Path != "/remote/report.txt" {
		t.Fatalf("conflict = %#v", request)
	}
	completed := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
	got, _ := os.ReadFile(target)
	if completed.State != operations.Completed || string(got) != "new" {
		t.Fatalf("replace = %#v %q", completed, got)
	}
}

type blockingDownloadReader struct {
	ctx     context.Context
	entered chan struct{}
}

func (r *blockingDownloadReader) Read([]byte) (int, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func (*blockingDownloadReader) Close() error { return nil }

type brokenDownloadReader struct{ sent bool }

func (r *brokenDownloadReader) Read(buffer []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(buffer, []byte("partial")), nil
}
func (*brokenDownloadReader) Close() error { return nil }

func assertOldAndNoTemporary(t *testing.T, directory, target string) {
	t.Helper()
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old" {
		t.Fatalf("old destination changed: %q, %v", got, err)
	}
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if len(entry.Name()) >= len(".marten-copy-") && entry.Name()[:len(".marten-copy-")] == ".marten-copy-" {
			t.Fatalf("temporary leaked: %s", entry.Name())
		}
	}
}

func TestDownloadCancellationAndDisconnectPreserveDestination(t *testing.T) {
	for _, test := range []struct {
		name   string
		open   func(context.Context, string) (io.ReadCloser, error)
		cancel bool
	}{
		{"cancel", func(ctx context.Context, _ string) (io.ReadCloser, error) {
			return &blockingDownloadReader{ctx: ctx, entered: make(chan struct{}, 1)}, nil
		}, true},
		{"disconnect", func(context.Context, string) (io.ReadCloser, error) { return &brokenDownloadReader{}, nil }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := t.TempDir()
			target := filepath.Join(destination, "file.bin")
			_ = os.WriteFile(target, []byte("old"), 0o644)
			open := test.open
			var entered chan struct{}
			if test.cancel {
				entered = make(chan struct{}, 1)
				open = func(ctx context.Context, _ string) (io.ReadCloser, error) {
					return &blockingDownloadReader{ctx: ctx, entered: entered}, nil
				}
			}
			remote := &downloadRemote{entries: map[string]core.FileEntry{"/remote/file.bin": remoteEntry("file.bin", core.KindFile, 1024)}, open: open}
			manager, operation := startDownload(t, remote, localfs.LocalFileSystem{}, []string{"/remote/file.bin"}, destination)
			resolveNext(t, manager, operation.ID, operations.Replace, false)
			var finished operations.Operation
			if test.cancel {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("download did not start streaming")
				}
				_, _ = manager.Cancel(operation.ID)
				// Cancel is observable immediately; Close waits for the runner's
				// deferred cleanup before inspecting the filesystem.
				manager.Close()
				finished, _ = manager.Get(operation.ID)
			} else {
				finished = waitCopyState(t, manager, operation.ID, operations.Cancelled, operations.Failed)
				manager.Close()
			}
			if test.cancel && finished.State != operations.Cancelled || !test.cancel && finished.State != operations.Failed {
				t.Fatalf("state = %s", finished.State)
			}
			assertOldAndNoTemporary(t, destination, target)
		})
	}
}

type noSpaceDestination struct{ localfs.LocalFileSystem }

func (n noSpaceDestination) CreateTemporaryFile(ctx context.Context, directory string, mode fs.FileMode) (core.TemporaryFile, error) {
	file, err := n.LocalFileSystem.CreateTemporaryFile(ctx, directory, mode)
	if err != nil {
		return nil, err
	}
	return &noSpaceFile{TemporaryFile: file}, nil
}

type noSpaceFile struct{ core.TemporaryFile }

func (f *noSpaceFile) Write([]byte) (int, error) { return 0, syscall.ENOSPC }

func TestDownloadNoSpaceAndUnsupportedKinds(t *testing.T) {
	destination := t.TempDir()
	target := filepath.Join(destination, "file.bin")
	_ = os.WriteFile(target, []byte("old"), 0o644)
	remote := &downloadRemote{entries: map[string]core.FileEntry{
		"/remote/file.bin": remoteEntry("file.bin", core.KindFile, 10),
		"/remote/dir":      remoteEntry("dir", core.KindDirectory, 0),
		"/remote/link":     remoteEntry("link", core.KindSymlink, 0),
	}, data: map[string][]byte{"/remote/file.bin": []byte("new content")}}
	manager, operation := startDownload(t, remote, noSpaceDestination{}, []string{"/remote/file.bin"}, destination)
	resolveNext(t, manager, operation.ID, operations.Replace, false)
	failed := waitCopyState(t, manager, operation.ID, operations.Failed)
	manager.Close()
	if failed.Error == nil || failed.Error.Code != "download_failed" {
		t.Fatalf("failure = %#v", failed)
	}
	assertOldAndNoTemporary(t, destination, target)
	for _, source := range []string{"/remote/dir", "/remote/link"} {
		manager := operations.NewManager()
		_, err := NewDownloadService(remote, localfs.LocalFileSystem{}, manager).Start(context.Background(), operations.ContextSnapshot{}, "session-1", []string{source}, destination)
		manager.Close()
		if !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("%s error = %v", source, err)
		}
	}
}
