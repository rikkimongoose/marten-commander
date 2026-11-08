//go:build linux

package desktop

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/app"
)

func TestLinuxTildePathUsesHomeBeforeRelativeBase(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	t.Setenv("HOME", home)
	provider := &source{}
	service := NewDirectoryService(app.NewDirectoryService(provider, provider, base), false)

	for input, want := range map[string]string{
		"~":          home,
		"~/":         home,
		"~/Projects": filepath.Join(home, "Projects"),
		"~other":     filepath.Join(base, "~other"),
		"dir/~":      filepath.Join(base, "dir", "~"),
	} {
		if _, err := service.ReadDirectory(context.Background(), DirectoryRequest{Path: input, BaseLocation: encode(base)}); err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if provider.seen != want {
			t.Errorf("%q resolved to %q, want %q", input, provider.seen, want)
		}
	}
}
