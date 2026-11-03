package localfs_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0640); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0750); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" && (errors.Is(err, fs.ErrPermission) || errors.Is(err, errors.ErrUnsupported)) {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func assertCause(t *testing.T, err, want error, path string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want cause %v", err, want)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Path != path {
		t.Fatalf("error = %v, want PathError for %q", err, path)
	}
}

func TestReadDirAndLstat(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "report.txt"), "hello\n")
	writeFile(t, filepath.Join(dir, ".hidden"), "hidden")
	mkdir(t, filepath.Join(dir, "folder"))
	filesystem := localfs.LocalFileSystem{}
	entries, err := filesystem.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		seen[entry.Name] = true
		path := filepath.Join(dir, entry.Name)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Path != path || entry.Size != info.Size() || entry.Mode != info.Mode() || !entry.ModifiedAt.Equal(info.ModTime()) {
			t.Errorf("metadata mismatch: %+v, OS metadata: %+v", entry, info)
		}
		wantKind := core.KindFile
		if entry.Name == "folder" {
			wantKind = core.KindDirectory
		}
		if entry.Kind != wantKind {
			t.Errorf("kind of %q = %q, want %q", entry.Name, entry.Kind, wantKind)
		}
		stat, err := filesystem.Lstat(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if stat != entry {
			t.Errorf("Lstat = %+v, ReadDir = %+v", stat, entry)
		}
	}
	for _, name := range []string{"report.txt", ".hidden", "folder"} {
		if !seen[name] {
			t.Errorf("missing %q", name)
		}
	}
}

func TestReadDirEmptyAndMultipleBatches(t *testing.T) {
	filesystem := localfs.LocalFileSystem{}
	dir := t.TempDir()
	entries, err := filesystem.ReadDir(context.Background(), dir)
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatalf("empty directory: entries = %v, err = %v", entries, err)
	}
	const count = 301
	for i := range count {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("file-%03d", i)), "")
	}
	entries, err = filesystem.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if seen[entry.Name] {
			t.Fatalf("duplicate entry %q", entry.Name)
		}
		seen[entry.Name] = true
	}
	if len(seen) != count {
		t.Fatalf("got %d unique entries, want %d", len(seen), count)
	}
}

func TestUnusualNames(t *testing.T) {
	names := []string{"with spaces.txt", "кириллица 日本語 🦊.txt", "-leading-dash", "$(command);'quote'.txt", "e\u0301.txt"}
	if runtime.GOOS != "windows" {
		names = append(names, "line\nbreak\t.txt", "back\\slash:*?\".txt")
	}
	dir := t.TempDir()
	for _, name := range names {
		writeFile(t, filepath.Join(dir, name), name)
	}
	filesystem := localfs.LocalFileSystem{}
	entries, err := filesystem.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Fatalf("got %d entries, want %d", len(entries), len(names))
	}
	for _, entry := range entries {
		assertContents(t, filesystem, entry.Path, entry.Name)
	}
}

func assertContents(t *testing.T, filesystem core.FileSystem, path, want string) {
	t.Helper()
	r, err := filesystem.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read = %v, close = %v", readErr, closeErr)
	}
	if string(data) != want {
		t.Fatalf("contents of %q = %q, want %q", path, data, want)
	}
}

func TestOpenReadOnlyAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	writeFile(t, path, "original content")
	filesystem := localfs.LocalFileSystem{}
	r, err := filesystem.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, writable := r.(io.Writer); writable {
		t.Error("reader unexpectedly exposes io.Writer")
	}
	buf := make([]byte, 4)
	if n, err := r.Read(buf); n != 4 || err != nil || string(buf) != "orig" {
		t.Errorf("first read = %d, %q, %v", n, buf, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = r.Read(buf)
	assertCause(t, err, fs.ErrClosed, path)
	assertContents(t, filesystem, path, "original content")
}

func TestSymlinks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "file"), "target data")
	mkdir(t, filepath.Join(dir, "folder"))
	writeFile(t, filepath.Join(dir, "folder", "child"), "child data")
	links := map[string]string{"file-link": "file", "dir-link": "folder", "dangling": "missing", "loop": "loop"}
	for name, target := range links {
		symlink(t, target, filepath.Join(dir, name))
	}
	filesystem := localfs.LocalFileSystem{}
	entries, err := filesystem.ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, entry := range entries {
		target, isLink := links[entry.Name]
		if !isLink {
			continue
		}
		seen++
		if entry.Kind != core.KindSymlink || entry.Mode&fs.ModeSymlink == 0 {
			t.Errorf("link followed in listing: %+v", entry)
		}
		stat, err := filesystem.Lstat(context.Background(), entry.Path)
		if err != nil || stat.Kind != core.KindSymlink {
			t.Fatalf("Lstat link: %+v, %v", stat, err)
		}
		got, err := filesystem.Readlink(context.Background(), entry.Path)
		if err != nil || got != target {
			t.Errorf("Readlink = %q, %v; want literal %q", got, err, target)
		}
	}
	if seen != len(links) {
		t.Fatalf("got %d links, want %d", seen, len(links))
	}
	assertContents(t, filesystem, filepath.Join(dir, "file-link"), "target data")
	children, err := filesystem.ReadDir(context.Background(), filepath.Join(dir, "dir-link"))
	if err != nil || len(children) != 1 || children[0].Name != "child" {
		t.Fatalf("directory link: %v, %v", children, err)
	}
	assertContents(t, filesystem, children[0].Path, "child data")
	dangling := filepath.Join(dir, "dangling")
	_, err = filesystem.Open(context.Background(), dangling)
	assertCause(t, err, fs.ErrNotExist, dangling)
	if _, err := filesystem.Open(context.Background(), filepath.Join(dir, "loop")); err == nil {
		t.Fatal("opening a symlink loop succeeded")
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, file, "data")
	filesystem := localfs.LocalFileSystem{}
	missing := filepath.Join(dir, "missing")
	_, err := filesystem.ReadDir(context.Background(), missing)
	assertCause(t, err, fs.ErrNotExist, missing)
	_, err = filesystem.Lstat(context.Background(), missing)
	assertCause(t, err, fs.ErrNotExist, missing)
	_, err = filesystem.Open(context.Background(), missing)
	assertCause(t, err, fs.ErrNotExist, missing)
	_, err = filesystem.Readlink(context.Background(), missing)
	assertCause(t, err, fs.ErrNotExist, missing)
	_, err = filesystem.ReadDir(context.Background(), file)
	assertCause(t, err, fs.ErrInvalid, file)
	_, err = filesystem.Open(context.Background(), dir)
	assertCause(t, err, fs.ErrInvalid, dir)
	if _, err := filesystem.Readlink(context.Background(), file); err == nil {
		t.Fatal("Readlink on a regular file succeeded")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, err = filesystem.Lstat(context.Background(), file)
	assertCause(t, err, fs.ErrNotExist, file)
}

func TestContextAndPathValidation(t *testing.T) {
	filesystem := localfs.LocalFileSystem{}
	methods := map[string]func(context.Context, string) error{
		"ReadDir": func(ctx context.Context, path string) error {
			_, err := filesystem.ReadDir(ctx, path)
			return err
		},
		"Lstat": func(ctx context.Context, path string) error {
			_, err := filesystem.Lstat(ctx, path)
			return err
		},
		"Open": func(ctx context.Context, path string) error {
			r, err := filesystem.Open(ctx, path)
			if r != nil {
				err = errors.Join(err, r.Close())
			}
			return err
		},
		"Readlink": func(ctx context.Context, path string) error {
			_, err := filesystem.Readlink(ctx, path)
			return err
		},
		"ResolvePath": func(ctx context.Context, path string) error {
			_, err := filesystem.ResolvePath(ctx, path)
			return err
		},
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for name, method := range methods {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"", "relative/path"} {
				assertCause(t, method(context.Background(), path), fs.ErrInvalid, path)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			assertCause(t, method(ctx, missing), context.Canceled, missing)
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			assertCause(t, method(ctx, missing), context.DeadlineExceeded, missing)
		})
	}
}

func TestCancelOpenReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	writeFile(t, path, "first second")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := (localfs.LocalFileSystem{}).Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	buf := make([]byte, 5)
	if n, err := r.Read(buf); n != 5 || err != nil || string(buf) != "first" {
		t.Fatalf("read before cancellation = %d, %q, %v", n, buf, err)
	}
	cancel()
	n, err := r.Read(buf)
	if n != 0 {
		t.Errorf("read %d bytes after cancellation", n)
	}
	assertCause(t, err, context.Canceled, path)
}

func TestConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	writeFile(t, path, "shared data")
	filesystem := localfs.LocalFileSystem{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 10 {
				entries, err := filesystem.ReadDir(context.Background(), dir)
				if err != nil || len(entries) != 1 {
					t.Errorf("concurrent ReadDir: %v, %v", entries, err)
					return
				}
				if _, err := filesystem.Lstat(context.Background(), path); err != nil {
					t.Error(err)
					return
				}
				r, err := filesystem.Open(context.Background(), path)
				if err != nil {
					t.Error(err)
					return
				}
				data, readErr := io.ReadAll(r)
				closeErr := r.Close()
				if string(data) != "shared data" || readErr != nil || closeErr != nil {
					t.Errorf("concurrent read: %q, %v, %v", data, readErr, closeErr)
					return
				}
			}
		})
	}
	wg.Wait()
}
