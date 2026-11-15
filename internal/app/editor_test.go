package app_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/settings"
)

type editorLauncherStub struct {
	config settings.EditorConfig
	path   string
	err    error
}

func (s *editorLauncherStub) LaunchEditor(config settings.EditorConfig, path string) error {
	s.config, s.path = config, path
	return s.err
}

func TestEditorServiceLaunchesExactReadableUnicodeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "текст с пробелами.txt")
	if err := os.WriteFile(path, []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &editorLauncherStub{}
	config := settings.EditorConfig{Mode: settings.EditorCustom, Executable: "editor", Arguments: []string{"{file}"}}
	if err := app.NewEditorService(localfs.LocalFileSystem{}, launcher, config, nil).Edit(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if launcher.path != path || launcher.config.Executable != "editor" {
		t.Fatalf("launch: %#v %q", launcher.config, launcher.path)
	}
}

func TestEditorServiceRejectsMissingDirectorySymlinkAndSpecialObjects(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	_ = os.WriteFile(regular, []byte("x"), 0o600)
	link := filepath.Join(root, "link")
	_ = os.Symlink(regular, link)
	fifo := filepath.Join(root, "fifo")
	launcher := &editorLauncherStub{}
	service := app.NewEditorService(localfs.LocalFileSystem{}, launcher, settings.EditorConfig{Mode: settings.EditorAuto}, nil)
	for _, path := range []string{filepath.Join(root, "missing"), root, link, fifo} {
		if err := service.Edit(context.Background(), path); err == nil {
			t.Errorf("Edit(%q) succeeded", path)
		}
	}
	if launcher.path != "" {
		t.Fatalf("launched unsupported object %q", launcher.path)
	}
}

type deniedEditorFS struct{}

func (deniedEditorFS) Lstat(_ context.Context, path string) (core.FileEntry, error) {
	return core.FileEntry{Path: path, Kind: core.KindFile}, nil
}
func (deniedEditorFS) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, fs.ErrPermission
}

type unsupportedEditorFS struct{}

func (unsupportedEditorFS) Lstat(_ context.Context, path string) (core.FileEntry, error) {
	return core.FileEntry{Path: path, Kind: core.KindOther}, nil
}
func (unsupportedEditorFS) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("must not open an unsupported object")
}

func TestEditorServiceReportsPermissionConfigAndLauncherErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	launcher := &editorLauncherStub{}
	service := app.NewEditorService(deniedEditorFS{}, launcher, settings.EditorConfig{Mode: settings.EditorAuto}, nil)
	if err := service.Edit(context.Background(), path); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission: %v", err)
	}
	configErr := errors.New("bad configuration")
	service = app.NewEditorService(deniedEditorFS{}, launcher, settings.EditorConfig{}, configErr)
	if err := service.Edit(context.Background(), path); !errors.Is(err, configErr) {
		t.Fatalf("config: %v", err)
	}
	launcherFailure := errors.New("start failed")
	launcher.err = launcherFailure
	localPath := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(localPath, []byte("x"), 0o600)
	service = app.NewEditorService(localfs.LocalFileSystem{}, launcher, settings.EditorConfig{Mode: settings.EditorAuto}, nil)
	if err := service.Edit(context.Background(), localPath); !errors.Is(err, launcherFailure) {
		t.Fatalf("launcher: %v", err)
	}
}

func TestEditorServiceRejectsSpecialObjectBeforeOpeningIt(t *testing.T) {
	launcher := &editorLauncherStub{}
	path := filepath.Join(t.TempDir(), "device")
	err := app.NewEditorService(unsupportedEditorFS{}, launcher, settings.EditorConfig{Mode: settings.EditorAuto}, nil).
		Edit(context.Background(), path)
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("special object: %v", err)
	}
	if launcher.path != "" {
		t.Fatalf("launched unsupported object %q", launcher.path)
	}
}
