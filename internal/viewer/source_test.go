package viewer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFixture(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalFileOpenMetadataAndRangeReads(t *testing.T) {
	path := writeFixture(t, []byte("0123456789"))
	file, err := (LocalFileSource{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	meta, err := file.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "fixture.bin" || meta.Path != path || meta.Size != 10 || meta.ModifiedAt == "" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	data, err := file.ReadAt(context.Background(), 2, 4)
	if err != nil || string(data) != "2345" {
		t.Fatalf("range = %q, %v", data, err)
	}
	data, err = file.ReadAt(context.Background(), 8, 8)
	if !errors.Is(err, io.EOF) || string(data) != "89" {
		t.Fatalf("partial EOF = %q, %v", data, err)
	}
	data, err = file.ReadAt(context.Background(), 100, 4)
	if !errors.Is(err, io.EOF) || len(data) != 0 {
		t.Fatalf("past EOF = %q, %v", data, err)
	}
}

func TestLocalFileEmptyAndBinaryData(t *testing.T) {
	for _, data := range [][]byte{{}, {0, 1, 0xff, 2}} {
		file, err := (LocalFileSource{}).Open(context.Background(), writeFixture(t, data))
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := file.ReadAt(context.Background(), 0, len(data)+1)
		_ = file.Close()
		if len(data) == 0 && !errors.Is(readErr, io.EOF) {
			t.Fatalf("empty read should return EOF: %v", readErr)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("binary round trip = %v, want %v", got, data)
		}
	}
}

func TestLocalFileValidatesRangesAndCancellation(t *testing.T) {
	file, err := (LocalFileSource{}).Open(context.Background(), writeFixture(t, []byte("data")))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.ReadAt(context.Background(), -1, 1); !errors.Is(err, ErrInvalidOffset) {
		t.Fatalf("negative offset: %v", err)
	}
	if _, err := file.ReadAt(context.Background(), 0, MaxReadSize+1); !errors.Is(err, ErrInvalidLength) {
		t.Fatalf("large request: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := file.ReadAt(ctx, 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	if _, err := file.ReadAt(context.Background(), 1<<40, 1); !errors.Is(err, io.EOF) {
		t.Fatalf("large offset: %v", err)
	}
}

func TestLocalFileReadsSparseFileBeyondTwoGiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large-sparse.bin")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	const markerOffset int64 = 3*1024*1024*1024 + 17
	if err := file.Truncate(markerOffset + 4); err != nil {
		_ = file.Close()
		t.Skipf("filesystem cannot create a sparse >2 GiB fixture: %v", err)
	}
	if _, err := file.WriteAt([]byte("tail"), markerOffset); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := (LocalFileSource{}).Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	meta, err := opened.Metadata(context.Background())
	if err != nil || meta.Size != markerOffset+4 {
		t.Fatalf("large metadata = %#v, %v", meta, err)
	}
	got, err := opened.ReadAt(context.Background(), markerOffset, 16)
	if !errors.Is(err, io.EOF) || string(got) != "tail" {
		t.Fatalf("large range = %q, %v", got, err)
	}
}

func TestLocalFileRejectsMissingDirectoriesAndSpecialFiles(t *testing.T) {
	source := LocalFileSource{}
	missing := filepath.Join(t.TempDir(), "gone")
	if _, err := source.Open(context.Background(), missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := source.Open(context.Background(), t.TempDir()); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("directory: %v", err)
	}
	if runtime.GOOS != "windows" {
		// A symlink to a regular file is intentionally accepted; a symlink to a
		// directory resolves to a non-regular object and is rejected.
		target := writeFixture(t, []byte("ok"))
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		file, err := source.Open(context.Background(), link)
		if err != nil {
			t.Fatalf("regular symlink: %v", err)
		}
		_ = file.Close()
		dirLink := filepath.Join(t.TempDir(), "dir-link")
		if err := os.Symlink(t.TempDir(), dirLink); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Open(context.Background(), dirLink); !errors.Is(err, ErrNotRegular) {
			t.Fatalf("directory symlink: %v", err)
		}
	}
}

func TestLocalFileCloseAndRepeatedOpenClose(t *testing.T) {
	path := writeFixture(t, []byte("data"))
	for range 20 {
		file, err := (LocalFileSource{}).Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := file.ReadAt(context.Background(), 0, 1); err == nil {
			t.Fatal("read after close unexpectedly succeeded")
		}
	}
}

func TestLocalFilePermissionError(t *testing.T) {
	path := writeFixture(t, []byte("private"))
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	file, err := (LocalFileSource{}).Open(context.Background(), path)
	if err == nil {
		_ = file.Close()
		if os.Geteuid() != 0 {
			t.Fatal("permissionless file unexpectedly opened")
		}
	}
}
