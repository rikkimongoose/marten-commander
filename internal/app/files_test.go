package app_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
)

type fileInfoStub struct {
	entry core.FileEntry
	err   error
	seen  string
}

func (s *fileInfoStub) Lstat(_ context.Context, path string) (core.FileEntry, error) {
	s.seen = path
	return s.entry, s.err
}

type launcherStub struct {
	seen string
	err  error
}

func (s *launcherStub) OpenFile(path string) error {
	s.seen = path
	return s.err
}

func TestFileServiceOpensExactRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "отчёт с пробелами.txt")
	files := &fileInfoStub{entry: core.FileEntry{Path: path, Kind: core.KindFile}}
	launcher := &launcherStub{}
	if err := app.NewFileService(files, launcher).Open(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if files.seen != path || launcher.seen != path {
		t.Fatalf("path changed: stat=%q launch=%q want=%q", files.seen, launcher.seen, path)
	}
}

func TestFileServiceRejectsMissingAndNonRegularObjects(t *testing.T) {
	missing := errors.New("missing")
	for _, test := range []struct {
		name  string
		files *fileInfoStub
	}{
		{"missing", &fileInfoStub{err: missing}},
		{"directory", &fileInfoStub{entry: core.FileEntry{Kind: core.KindDirectory}}},
		{"symlink", &fileInfoStub{entry: core.FileEntry{Kind: core.KindSymlink}}},
		{"special", &fileInfoStub{entry: core.FileEntry{Kind: core.KindOther}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			launcher := &launcherStub{}
			err := app.NewFileService(test.files, launcher).Open(context.Background(), filepath.Join(t.TempDir(), test.name))
			if err == nil || launcher.seen != "" {
				t.Fatalf("Open = %v, launched %q", err, launcher.seen)
			}
			if test.name == "missing" && !errors.Is(err, missing) {
				t.Fatalf("missing cause lost: %v", err)
			}
			if test.name != "missing" && !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("invalid kind cause lost: %v", err)
			}
		})
	}
}

func TestFileServiceReportsLauncherFailure(t *testing.T) {
	failure := errors.New("no system handler")
	files := &fileInfoStub{entry: core.FileEntry{Kind: core.KindFile}}
	err := app.NewFileService(files, &launcherStub{err: failure}).Open(context.Background(), filepath.Join(t.TempDir(), "file.txt"))
	if !errors.Is(err, failure) {
		t.Fatalf("launcher cause lost: %v", err)
	}
}

func TestFileServiceWithLocalFilesystem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "файл с пробелами.txt")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &launcherStub{}
	service := app.NewFileService(localfs.LocalFileSystem{}, launcher)
	if err := service.Open(context.Background(), path); err != nil || launcher.seen != path {
		t.Fatalf("Open existing = %v, launched %q", err, launcher.seen)
	}
	launcher.seen = ""
	err := service.Open(context.Background(), filepath.Join(dir, "missing.txt"))
	if !errors.Is(err, fs.ErrNotExist) || launcher.seen != "" {
		t.Fatalf("Open missing = %v, launched %q", err, launcher.seen)
	}
}

func TestFileServiceHonoursCancellationBeforeLaunching(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	launcher := &launcherStub{}
	err := app.NewFileService(&fileInfoStub{entry: core.FileEntry{Kind: core.KindFile}}, launcher).Open(ctx, filepath.Join(t.TempDir(), "file.txt"))
	if !errors.Is(err, context.Canceled) || launcher.seen != "" {
		t.Fatalf("Open = %v, launched %q", err, launcher.seen)
	}
}
