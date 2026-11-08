package desktop

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
)

func TestLocalResolvedAncestryAndPermissionDenied(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	child := filepath.Join(home, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "home-link")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	filesystem := localfs.LocalFileSystem{}
	service := NewDirectoryService(app.NewDirectoryService(filesystem, filesystem, home), false)
	result, err := service.ReadDirectory(context.Background(), DirectoryRequest{Path: filepath.Join(alias, "child")})
	if err != nil || result.Location != encode(child) || result.AncestorLocations[0] != encode(home) {
		t.Fatalf("ancestry must use the resolved Home address: %+v, %v", result, err)
	}
	t.Run("permission denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions")
		}
		if err := os.Chmod(child, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(child, 0700); err != nil {
				t.Error(err)
			}
		})
		result, err := service.ReadDirectory(context.Background(), DirectoryRequest{Location: encode(child)})
		if err == nil || err.Error() != "Нет прав на чтение каталога "+display(child) || result.Entries != nil {
			t.Fatalf("permission denied must not return a partial snapshot: %+v, %v", result, err)
		}
		if _, err := service.ReadDirectory(context.Background(), DirectoryRequest{Location: encode(home)}); err != nil {
			t.Fatalf("another directory must remain readable: %v", err)
		}
	})
}
