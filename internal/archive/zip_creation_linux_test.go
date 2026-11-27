//go:build linux

package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"golang.org/x/sys/unix"
)

func TestZIPCreationRejectsNestedLinksAndSpecialFilesBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		build func(string) error
	}{
		{name: "symlink", build: func(name string) error { return os.Symlink("regular", name) }},
		{name: "fifo", build: func(name string) error { return unix.Mkfifo(name, 0o600) }},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			source := filepath.Join(base, "source")
			if err := os.Mkdir(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "regular"), []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := test.build(filepath.Join(source, "unsupported")); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(base, "archive.zip")
			if _, err := CreateZIP(context.Background(), localfs.LocalFileSystem{}, []string{source}, destination,
				ZIPCreationOptions{}); !errors.Is(err, ErrUnsupportedEntry) {
				t.Fatalf("error=%v", err)
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination appeared: %v", err)
			}
			assertNoCopyTemporaries(t, base)
		})
	}
}
