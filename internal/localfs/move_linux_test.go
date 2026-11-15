//go:build linux

package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

func TestRenameNewAndReplaceAreConditional(t *testing.T) {
	root := t.TempDir()
	filesystem := LocalFileSystem{}
	source, destination := filepath.Join(root, "источник"), filepath.Join(root, "назначение")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceFP, _ := filesystem.Fingerprint(context.Background(), source)
	destinationFP, _ := filesystem.Fingerprint(context.Background(), destination)
	if err := filesystem.RenameNew(context.Background(), source, destination, sourceFP); !errors.Is(err, os.ErrExist) {
		t.Fatalf("RenameNew must not overwrite: %v", err)
	}
	if err := filesystem.RenameReplace(context.Background(), source, destination, sourceFP, destinationFP); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remained: %v", err)
	}
	if content, _ := os.ReadFile(destination); string(content) != "new" {
		t.Fatalf("destination = %q", content)
	}
}

func TestRemoveSourceRejectsChangedObject(t *testing.T) {
	root := t.TempDir()
	filesystem := LocalFileSystem{}
	path := filepath.Join(root, "source")
	_ = os.WriteFile(path, []byte("before"), 0o600)
	fingerprint, _ := filesystem.Fingerprint(context.Background(), path)
	_ = os.WriteFile(path, []byte("changed content"), 0o600)
	if err := filesystem.RemoveSource(context.Background(), path, fingerprint); !errors.Is(err, core.ErrSourceChanged) {
		t.Fatalf("wanted source changed, got %v", err)
	}
	if content, _ := os.ReadFile(path); string(content) != "changed content" {
		t.Fatalf("changed source lost: %q", content)
	}
}
