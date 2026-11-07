//go:build linux

package localfs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/localfs"
)

func TestSystemFileLauncherPassesOneLiteralArgument(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argument")
	handler := filepath.Join(dir, "xdg-open")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > \"$CAPTURE\"\n"
	if err := os.WriteFile(handler, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CAPTURE", capture)
	path := filepath.Join(dir, "файл с пробелами.txt")
	if err := (localfs.SystemFileLauncher{}).OpenFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil || string(data) != path {
		t.Fatalf("argument = %q, %v; want %q", data, err, path)
	}
}

func TestSystemFileLauncherReportsMissingAndFailingHandler(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if err := (localfs.SystemFileLauncher{}).OpenFile(filepath.Join(dir, "file.txt")); err == nil {
		t.Fatal("missing xdg-open succeeded")
	}
	handler := filepath.Join(dir, "xdg-open")
	if err := os.WriteFile(handler, []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (localfs.SystemFileLauncher{}).OpenFile(filepath.Join(dir, "file.txt")); err == nil {
		t.Fatal("failing xdg-open succeeded")
	}
}
