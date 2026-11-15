package localfs_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
)

func TestNonUTF8Names(t *testing.T) {
	dir := t.TempDir()
	name := "raw-\xff\xfe"
	path := filepath.Join(dir, name)
	writeFile(t, path, "raw name contents")
	filesystem := localfs.LocalFileSystem{}
	entries, err := filesystem.ReadDir(context.Background(), dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	if entries[0].Name != name || entries[0].Path != path {
		t.Fatalf("filename bytes changed: %+v", entries[0])
	}
	assertContents(t, filesystem, entries[0].Path, "raw name contents")
	stat, err := filesystem.Lstat(context.Background(), path)
	if err != nil || stat.Name != name {
		t.Fatalf("Lstat = %+v, %v", stat, err)
	}
}

func TestSymlinkParentPathPreserved(t *testing.T) {
	dir := t.TempDir()
	physical := filepath.Join(dir, "physical")
	mkdir(t, physical)
	mkdir(t, filepath.Join(physical, "nested"))
	writeFile(t, filepath.Join(physical, "file"), "physical parent")
	writeFile(t, filepath.Join(dir, "file"), "lexical parent")
	link := filepath.Join(dir, "link")
	symlink(t, filepath.Join(physical, "nested"), link)
	// filepath.Join would erase the symlink/.. segment and address another dir.
	path := link + "/.."
	filesystem := localfs.LocalFileSystem{}
	entries, err := filesystem.ReadDir(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == "file" {
			assertContents(t, filesystem, entry.Path, "physical parent")
			return
		}
	}
	t.Fatal("file missing")
}

func TestSpecialFileRejectedWithoutOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	filesystem := localfs.LocalFileSystem{}
	stat, err := filesystem.Lstat(context.Background(), path)
	if err != nil || stat.Kind != core.KindOther {
		t.Fatalf("FIFO metadata = %+v, %v", stat, err)
	}
	_, err = filesystem.Open(context.Background(), path)
	assertCause(t, err, fs.ErrInvalid, path)
	_, err = filesystem.ReadDir(context.Background(), path)
	assertCause(t, err, fs.ErrInvalid, path)
}

func TestPermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary permission checks")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "private-file")
	writeFile(t, file, "private")
	privateDir := filepath.Join(dir, "private-directory")
	mkdir(t, privateDir)
	child := filepath.Join(privateDir, "child")
	writeFile(t, child, "private")
	for _, path := range []string{file, privateDir} {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(path, 0700); err != nil {
				t.Error(err)
			}
		})
	}
	filesystem := localfs.LocalFileSystem{}
	_, err := filesystem.Open(context.Background(), file)
	assertCause(t, err, fs.ErrPermission, file)
	entries, err := filesystem.ReadDir(context.Background(), privateDir)
	assertCause(t, err, fs.ErrPermission, privateDir)
	if entries != nil {
		t.Errorf("failed listing returned entries: %v", entries)
	}
	_, err = filesystem.Lstat(context.Background(), child)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Lstat inaccessible child = %v", err)
	}
}

func TestCreateDirectoryPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary permission checks")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	err := (localfs.LocalFileSystem{}).CreateDirectory(context.Background(), parent, "denied")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("CreateDirectory = %v, want permission denied", err)
	}
}
