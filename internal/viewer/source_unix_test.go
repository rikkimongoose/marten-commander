//go:build !windows

package viewer

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLocalFileRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (LocalFileSource{}).Open(context.Background(), path); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("FIFO: %v", err)
	}
}
