// Package localfs implements filesystem contracts using the host operating system.
package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

// LocalFileSystem reads absolute host-native paths. Its zero value is ready to
// use and may be shared by goroutines. It does not change the working directory.
type LocalFileSystem struct{}

var (
	_ core.FileSystem   = LocalFileSystem{}
	_ core.LinkReader   = LocalFileSystem{}
	_ core.PathResolver = LocalFileSystem{}
)

// ReadDir follows a directory symlink to allow navigation, but never follows the
// links in the listing. Batches bound the work between cancellation checks.
func (LocalFileSystem) ReadDir(ctx context.Context, path string) (entries []core.FileEntry, err error) {
	f, err := openKind(ctx, path, core.KindDirectory)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			entries = nil
		}
	}()

	entries = make([]core.FileEntry, 0)
	for {
		if err := check(ctx, "readdir", path); err != nil {
			return nil, err
		}
		batch, readErr := f.ReadDir(128)
		for _, item := range batch {
			entryPath := childPath(path, item.Name())
			if err := check(ctx, "readdir", entryPath); err != nil {
				return nil, err
			}
			info, err := item.Info()
			if err != nil {
				return nil, pathError("readdir", entryPath, err)
			}
			entries = append(entries, entry(entryPath, info))
		}
		if err := check(ctx, "readdir", path); err != nil {
			return nil, err
		}
		if errors.Is(readErr, io.EOF) {
			return entries, nil
		}
		if readErr != nil {
			return nil, pathError("readdir", path, readErr)
		}
	}
}

func (LocalFileSystem) Lstat(ctx context.Context, path string) (core.FileEntry, error) {
	if err := check(ctx, "lstat", path); err != nil {
		return core.FileEntry{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return core.FileEntry{}, err
	}
	if err := check(ctx, "lstat", path); err != nil {
		return core.FileEntry{}, err
	}
	return entry(path, info), nil
}

func (LocalFileSystem) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	f, err := openKind(ctx, path, core.KindFile)
	if err != nil {
		return nil, err
	}
	return &contextFile{ctx: ctx, file: f}, nil
}

func (LocalFileSystem) Readlink(ctx context.Context, path string) (string, error) {
	if err := check(ctx, "readlink", path); err != nil {
		return "", err
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if err := check(ctx, "readlink", path); err != nil {
		return "", err
	}
	return target, nil
}

func (LocalFileSystem) ResolvePath(ctx context.Context, path string) (string, error) {
	if err := check(ctx, "resolve", path); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if err := check(ctx, "resolve", path); err != nil {
		return "", err
	}
	return resolved, nil
}

func entry(path string, info fs.FileInfo) core.FileEntry {
	return core.FileEntry{
		Path:       path,
		Name:       info.Name(),
		Kind:       core.KindFromMode(info.Mode()),
		Size:       info.Size(),
		ModifiedAt: info.ModTime(),
		Mode:       info.Mode(),
		Hidden:     isHidden(info),
	}
}

// Unlike filepath.Join, this must not clean an existing "symlink/.." prefix:
// that would change which file the address refers to on Unix.
func childPath(dir, name string) string {
	if os.IsPathSeparator(dir[len(dir)-1]) {
		return dir + name
	}
	return dir + string(os.PathSeparator) + name
}

func check(ctx context.Context, op, path string) error {
	if err := ctx.Err(); err != nil {
		return pathError(op, path, err)
	}
	if !filepath.IsAbs(path) {
		return pathError(op, path, fmt.Errorf("%w: an absolute path is required", fs.ErrInvalid))
	}
	return nil
}

func pathError(op, path string, err error) error {
	return &fs.PathError{Op: op, Path: path, Err: err}
}

// openKind rejects known special files before opening and checks the actual
// descriptor afterwards. It is not a security boundary against concurrent path
// replacement; context cannot interrupt every blocking OS call.
func openKind(ctx context.Context, path string, want core.EntryKind) (*os.File, error) {
	if err := check(ctx, "open", path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if core.KindFromMode(info.Mode()) != want {
		return nil, wrongKind(path, want)
	}
	if err := check(ctx, "open", path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err == nil && core.KindFromMode(info.Mode()) != want {
		err = wrongKind(path, want)
	}
	if err == nil {
		err = check(ctx, "open", path)
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func wrongKind(path string, want core.EntryKind) error {
	return pathError("open", path, fmt.Errorf("%w: expected %s", fs.ErrInvalid, want))
}

type contextFile struct {
	ctx  context.Context
	file *os.File
}

func (f *contextFile) Read(p []byte) (int, error) {
	if err := check(f.ctx, "read", f.file.Name()); err != nil {
		return 0, err
	}
	n, err := f.file.Read(p)
	if cancelErr := check(f.ctx, "read", f.file.Name()); cancelErr != nil {
		return n, cancelErr
	}
	return n, err
}

func (f *contextFile) Close() error { return f.file.Close() }
