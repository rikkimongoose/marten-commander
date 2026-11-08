package core_test

import (
	"io/fs"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

func TestKindFromMode(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode fs.FileMode
		want core.EntryKind
	}{
		{"regular", 0644, core.KindFile},
		{"executable", 0755, core.KindFile},
		{"setuid regular", fs.ModeSetuid | 0755, core.KindFile},
		{"directory", fs.ModeDir | 0755, core.KindDirectory},
		{"symlink", fs.ModeSymlink | 0777, core.KindSymlink},
		{"directory symlink", fs.ModeSymlink | fs.ModeDir, core.KindSymlink},
		{"fifo", fs.ModeNamedPipe | 0600, core.KindOther},
		{"socket", fs.ModeSocket | 0600, core.KindOther},
		{"device", fs.ModeDevice | 0600, core.KindOther},
		{"character device", fs.ModeDevice | fs.ModeCharDevice, core.KindOther},
		{"irregular", fs.ModeIrregular, core.KindOther},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := core.KindFromMode(tt.mode); got != tt.want {
				t.Fatalf("KindFromMode(%v) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}
