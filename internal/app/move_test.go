package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

func startMove(t *testing.T, filesystem moveFilesystem, sources []string, destination string) (*operations.Manager, operations.Operation) {
	t.Helper()
	manager := operations.NewManager()
	op, err := NewMoveService(filesystem, manager).Start(context.Background(), operations.ContextSnapshot{}, sources, destination)
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	return manager, op
}

func TestMoveRenameFilesDirectoriesUnicodeAndMultipleSelection(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(source, 0o755)
	_ = os.Mkdir(destination, 0o755)
	file := filepath.Join(source, "отчёт с пробелами 🦊.txt")
	directory := filepath.Join(source, "дерево")
	_ = os.WriteFile(file, []byte("data"), 0o644)
	_ = os.MkdirAll(filepath.Join(directory, "nested"), 0o755)
	_ = os.WriteFile(filepath.Join(directory, "nested", "file"), []byte("nested"), 0o644)
	manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{file, directory}, destination)
	defer manager.Close()
	completed := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
	if completed.State != operations.Completed {
		t.Fatalf("move failed: %#v", completed.Error)
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source file remained")
	}
	if content, _ := os.ReadFile(filepath.Join(destination, filepath.Base(file))); string(content) != "data" {
		t.Fatalf("unicode file not moved")
	}
	if content, _ := os.ReadFile(filepath.Join(destination, "дерево", "nested", "file")); string(content) != "nested" {
		t.Fatalf("tree not moved")
	}
}

func TestMoveSingleSourceCanRenameFileAndNonEmptyDirectory(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(string) error
		check  func(*testing.T, string)
	}{
		{"file with Unicode", func(path string) error { return os.WriteFile(path, []byte("x"), 0o644) }, func(t *testing.T, path string) {
			if content, err := os.ReadFile(path); err != nil || string(content) != "x" {
				t.Fatalf("file: %q %v", content, err)
			}
		}},
		{"non-empty directory", func(path string) error {
			if err := os.MkdirAll(filepath.Join(path, "nested"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(path, "nested", "inside.txt"), []byte("nested"), 0o644)
		}, func(t *testing.T, path string) {
			if content, err := os.ReadFile(filepath.Join(path, "nested", "inside.txt")); err != nil || string(content) != "nested" {
				t.Fatalf("directory content: %q %v", content, err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "старое имя")
			target := filepath.Join(root, "новое имя 🦊")
			if err := test.create(source); err != nil {
				t.Fatal(err)
			}
			manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{source}, target)
			defer manager.Close()
			if result := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed); result.State != operations.Completed {
				t.Fatalf("rename: %#v", result.Error)
			}
			if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source remained: %v", err)
			}
			test.check(t, target)
		})
	}
}

func TestMoveSingleFileToNewPathInAnotherDirectory(t *testing.T) {
	root := t.TempDir()
	sourceDirectory, targetDirectory := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.Mkdir(sourceDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(targetDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDirectory, "report.txt")
	target := filepath.Join(targetDirectory, "renamed.txt")
	if err := os.WriteFile(source, []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{source}, target)
	defer manager.Close()
	if result := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed); result.State != operations.Completed {
		t.Fatalf("move and rename: %#v", result.Error)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "report" {
		t.Fatalf("target: %q %v", content, err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remained: %v", err)
	}
}

func TestMoveRenameCollisionWaitsForExplicitResolution(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{source}, target)
	defer manager.Close()
	conflict := waitCopyState(t, manager, operation.ID, operations.WaitingForUser)
	if conflict.PendingConflict == nil || conflict.PendingConflict.Destination.Path != target {
		t.Fatalf("rename conflict: %#v", conflict.PendingConflict)
	}
	resolveNext(t, manager, operation.ID, operations.Skip, false)
	if result := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed); result.State != operations.Completed {
		t.Fatalf("skip: %#v", result.Error)
	}
	if content, _ := os.ReadFile(source); string(content) != "source" {
		t.Fatalf("source changed: %q", content)
	}
	if content, _ := os.ReadFile(target); string(content) != "existing" {
		t.Fatalf("destination overwritten without permission: %q", content)
	}
}

type exdevMoveFS struct {
	localfs.LocalFileSystem
	removeErr          error
	changeBeforeRemove bool
}

func (f *exdevMoveFS) RenameNew(context.Context, string, string, core.FileFingerprint) error {
	return syscall.EXDEV
}
func (f *exdevMoveFS) RenameReplace(context.Context, string, string, core.FileFingerprint, core.FileFingerprint) error {
	return syscall.EXDEV
}
func (f *exdevMoveFS) RemoveSource(ctx context.Context, source string, expected core.FileFingerprint) error {
	if f.changeBeforeRemove {
		_ = os.WriteFile(source, []byte("changed"), 0o644)
	}
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.LocalFileSystem.RemoveSource(ctx, source, expected)
}

func TestMoveEXDEVCopiesThenDeletesAndMovesSymlinkAsLink(t *testing.T) {
	root := t.TempDir()
	sourceDir, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(sourceDir, 0o755)
	_ = os.Mkdir(destination, 0o755)
	file := filepath.Join(sourceDir, "file")
	link := filepath.Join(sourceDir, "link")
	_ = os.WriteFile(file, []byte("data"), 0o644)
	_ = os.Symlink("file", link)
	manager, operation := startMove(t, &exdevMoveFS{}, []string{file, link}, destination)
	defer manager.Close()
	result := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
	if result.State != operations.Completed {
		t.Fatalf("cross-fs move: %#v", result.Error)
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source file remained")
	}
	if target, err := os.Readlink(filepath.Join(destination, "link")); err != nil || target != "file" {
		t.Fatalf("link: %q %v", target, err)
	}
}

func TestMoveConflictReplaceSkipKeepBoth(t *testing.T) {
	for _, action := range []operations.ConflictAction{operations.Replace, operations.Skip, operations.KeepBoth} {
		t.Run(string(action), func(t *testing.T) {
			root := t.TempDir()
			sourceDir, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
			_ = os.Mkdir(sourceDir, 0o755)
			_ = os.Mkdir(destination, 0o755)
			source, target := filepath.Join(sourceDir, "same.txt"), filepath.Join(destination, "same.txt")
			_ = os.WriteFile(source, []byte("new"), 0o644)
			_ = os.WriteFile(target, []byte("old"), 0o644)
			manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{source}, destination)
			defer manager.Close()
			resolveNext(t, manager, operation.ID, action, false)
			result := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
			if result.State != operations.Completed {
				t.Fatalf("move: %#v", result.Error)
			}
			switch action {
			case operations.Replace:
				if content, _ := os.ReadFile(target); string(content) != "new" {
					t.Fatalf("replace = %q", content)
				}
				if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("replace retained source")
				}
			case operations.Skip:
				if content, _ := os.ReadFile(source); string(content) != "new" {
					t.Fatal("skip lost source")
				}
			case operations.KeepBoth:
				if content, _ := os.ReadFile(filepath.Join(destination, "same (1).txt")); string(content) != "new" {
					t.Fatalf("keep both = %q", content)
				}
			}
		})
	}
}

func TestMoveMergeWithSkippedNestedFileKeepsSourceDirectory(t *testing.T) {
	root := t.TempDir()
	sourceRoot, destinationRoot := filepath.Join(root, "source"), filepath.Join(root, "destination")
	source, destination := filepath.Join(sourceRoot, "tree"), filepath.Join(destinationRoot, "tree")
	_ = os.MkdirAll(source, 0o755)
	_ = os.MkdirAll(destination, 0o755)
	_ = os.WriteFile(filepath.Join(source, "same"), []byte("new"), 0o644)
	_ = os.WriteFile(filepath.Join(destination, "same"), []byte("old"), 0o644)
	manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{source}, destinationRoot)
	defer manager.Close()
	resolveNext(t, manager, operation.ID, operations.Merge, false)
	resolveNext(t, manager, operation.ID, operations.Skip, false)
	result := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
	if result.State != operations.Completed {
		t.Fatalf("merge: %#v", result.Error)
	}
	if content, _ := os.ReadFile(filepath.Join(source, "same")); string(content) != "new" {
		t.Fatal("skipped source lost")
	}
	if content, _ := os.ReadFile(filepath.Join(destination, "same")); string(content) != "old" {
		t.Fatal("destination changed")
	}
}

func TestMoveCancelWhileWaitingKeepsSource(t *testing.T) {
	root := t.TempDir()
	sourceDir, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(sourceDir, 0o755)
	_ = os.Mkdir(destination, 0o755)
	source := filepath.Join(sourceDir, "same")
	_ = os.WriteFile(source, []byte("new"), 0o644)
	_ = os.WriteFile(filepath.Join(destination, "same"), []byte("old"), 0o644)
	manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{source}, destination)
	defer manager.Close()
	resolveNext(t, manager, operation.ID, operations.CancelOperation, false)
	waitCopyState(t, manager, operation.ID, operations.Cancelled)
	if content, _ := os.ReadFile(source); string(content) != "new" {
		t.Fatal("cancel lost source")
	}
}

func TestMoveReportsPartialWhenDeleteFailsOrSourceChanges(t *testing.T) {
	for _, test := range []struct {
		name       string
		filesystem *exdevMoveFS
		contains   string
	}{
		{"delete failure", &exdevMoveFS{removeErr: fs.ErrPermission}, "не удалён"},
		{"changed source", &exdevMoveFS{changeBeforeRemove: true}, "source changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			sourceDir, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
			_ = os.Mkdir(sourceDir, 0o755)
			_ = os.Mkdir(destination, 0o755)
			source := filepath.Join(sourceDir, "file")
			_ = os.WriteFile(source, []byte("data"), 0o644)
			manager, operation := startMove(t, test.filesystem, []string{source}, destination)
			defer manager.Close()
			result := waitCopyState(t, manager, operation.ID, operations.Failed)
			if result.Error == nil || result.Error.Code != "move_partial" || !strings.Contains(result.Error.Message, test.contains) {
				t.Fatalf("partial: %#v", result.Error)
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatalf("source lost: %v", err)
			}
			if _, err := os.Stat(filepath.Join(destination, "file")); err != nil {
				t.Fatalf("verified copy missing: %v", err)
			}
		})
	}
}

func TestMoveRejectsDirectoryInsideItselfAndMultipleTargetFile(t *testing.T) {
	root := t.TempDir()
	source, inside := filepath.Join(root, "source"), filepath.Join(root, "source", "inside")
	_ = os.MkdirAll(inside, 0o755)
	manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{source}, inside)
	defer manager.Close()
	result := waitCopyState(t, manager, operation.ID, operations.Failed)
	if result.Error == nil || !strings.Contains(result.Error.Message, "внутрь самого себя") {
		t.Fatalf("self move: %#v", result.Error)
	}
	file := filepath.Join(root, "target")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	service := NewMoveService(localfs.LocalFileSystem{}, operations.NewManager())
	if _, err := service.Start(context.Background(), operations.ContextSnapshot{}, []string{filepath.Join(source, "a"), filepath.Join(source, "b")}, file); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("multiple target: %v", err)
	}
}

func TestMoveRejectsExactSelfMoveWithoutDeletingIt(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "same")
	_ = os.WriteFile(source, []byte("safe"), 0o644)
	manager, operation := startMove(t, localfs.LocalFileSystem{}, []string{source}, source)
	defer manager.Close()
	result := waitCopyState(t, manager, operation.ID, operations.Failed)
	if result.Error == nil || !strings.Contains(result.Error.Message, "совпадают") {
		t.Fatalf("self move: %#v", result.Error)
	}
	if content, _ := os.ReadFile(source); string(content) != "safe" {
		t.Fatalf("self move deleted source: %q", content)
	}
}

func TestMoveApplyToAllIsReusedByCompatibleConflicts(t *testing.T) {
	root := t.TempDir()
	sourceDir, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(sourceDir, 0o755)
	_ = os.Mkdir(destination, 0o755)
	var sources []string
	for _, name := range []string{"one", "two"} {
		source := filepath.Join(sourceDir, name)
		sources = append(sources, source)
		_ = os.WriteFile(source, []byte("new-"+name), 0o644)
		_ = os.WriteFile(filepath.Join(destination, name), []byte("old"), 0o644)
	}
	manager, operation := startMove(t, localfs.LocalFileSystem{}, sources, destination)
	defer manager.Close()
	resolveNext(t, manager, operation.ID, operations.Replace, true)
	result := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed)
	if result.State != operations.Completed {
		t.Fatalf("apply all: %#v", result.Error)
	}
	for _, name := range []string{"one", "two"} {
		if content, _ := os.ReadFile(filepath.Join(destination, name)); string(content) != "new-"+name {
			t.Fatalf("%s = %q", name, content)
		}
	}
}

type slowMoveFS struct {
	exdevMoveFS
	started chan struct{}
	target  string
}

func (f *slowMoveFS) OpenCopyFile(ctx context.Context, path string) (io.ReadCloser, error) {
	reader, err := f.LocalFileSystem.OpenCopyFile(ctx, path)
	if err != nil {
		return nil, err
	}
	if path != f.target {
		return reader, nil
	}
	return &slowReadCloser{context: ctx, ReadCloser: reader, started: f.started}, nil
}

func TestMoveCancelDuringCrossFilesystemCopyKeepsSource(t *testing.T) {
	root := t.TempDir()
	sourceDir, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(sourceDir, 0o755)
	_ = os.Mkdir(destination, 0o755)
	source := filepath.Join(sourceDir, "large")
	_ = os.WriteFile(source, make([]byte, copyBufferSize*2), 0o644)
	filesystem := &slowMoveFS{started: make(chan struct{}), target: source}
	manager, operation := startMove(t, filesystem, []string{source}, destination)
	defer manager.Close()
	select {
	case <-filesystem.started:
	case <-time.After(time.Second):
		t.Fatal("copy did not start")
	}
	if _, err := manager.Cancel(operation.ID); err != nil {
		t.Fatal(err)
	}
	waitCopyState(t, manager, operation.ID, operations.Cancelled)
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("cancel removed source: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		entries, _ := os.ReadDir(destination)
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancel left destination entries: %#v", entries)
		}
		time.Sleep(time.Millisecond)
	}
}

type blockingRenameFS struct {
	localfs.LocalFileSystem
	started chan struct{}
	release chan struct{}
}

func (f *blockingRenameFS) RenameNew(ctx context.Context, source, destination string, expected core.FileFingerprint) error {
	close(f.started)
	<-f.release
	return f.LocalFileSystem.RenameNew(ctx, source, destination, expected)
}

func TestMoveSuccessfulAtomicRenameWinsCancellationRace(t *testing.T) {
	root := t.TempDir()
	sourceDir, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	_ = os.Mkdir(sourceDir, 0o755)
	_ = os.Mkdir(destination, 0o755)
	source := filepath.Join(sourceDir, "file")
	_ = os.WriteFile(source, []byte("data"), 0o644)
	filesystem := &blockingRenameFS{started: make(chan struct{}), release: make(chan struct{})}
	manager, operation := startMove(t, filesystem, []string{source}, destination)
	defer manager.Close()
	<-filesystem.started
	answer := make(chan operations.Operation, 1)
	go func() { result, _ := manager.Cancel(operation.ID); answer <- result }()
	select {
	case result := <-answer:
		t.Fatalf("cancel passed atomic rename early: %s", result.State)
	case <-time.After(10 * time.Millisecond):
	}
	close(filesystem.release)
	result := waitCopyState(t, manager, operation.ID, operations.Completed, operations.Failed, operations.Cancelled)
	if result.State != operations.Completed {
		t.Fatalf("atomic rename reported %s", result.State)
	}
	if cancelled := <-answer; cancelled.State != operations.Completed {
		t.Fatalf("cancel result = %s", cancelled.State)
	}
}
