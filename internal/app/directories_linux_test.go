package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
)

func TestLocalSymlinkNavigation(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "target", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"link": "target/nested", "broken": "absent", "loop": "loop"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	filesystem := localfs.LocalFileSystem{}
	service := app.NewDirectoryService(filesystem, filesystem, dir)
	result, err := service.ReadDirectory(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range result.Entries {
		if entry.Name == "link" && (!entry.Navigable || entry.Kind != core.KindSymlink || entry.TargetError != nil) {
			t.Errorf("directory link metadata: %+v", entry)
		}
		if (entry.Name == "broken" || entry.Name == "loop") && (entry.Navigable || entry.TargetError == nil) {
			t.Errorf("unavailable link metadata: %+v", entry)
		}
	}
	result, err = service.ReadDirectory(context.Background(), filepath.Join(dir, "link")+"/..")
	if err != nil || result.Path != filepath.Join(dir, "target") {
		t.Fatalf("symlink/.. navigation: %+v, %v", result, err)
	}
}
