//go:build linux

package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCopySourceNeverFollowsFinalSymlink(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	directory := filepath.Join(root, "directory")
	if err := os.WriteFile(file, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	fileLink, directoryLink := filepath.Join(root, "file-link"), filepath.Join(root, "directory-link")
	if err := os.Symlink(file, fileLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(directory, directoryLink); err != nil {
		t.Fatal(err)
	}
	filesystem := LocalFileSystem{}
	if reader, err := filesystem.OpenCopyFile(context.Background(), fileLink); err == nil {
		_ = reader.Close()
		t.Fatal("copy source followed a file symlink")
	}
	if entries, err := filesystem.ReadCopyDirectory(context.Background(), directoryLink); err == nil {
		t.Fatalf("copy source followed a directory symlink: %#v", entries)
	}
}
