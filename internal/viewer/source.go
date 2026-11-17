// Package viewer provides seekable, read-only file access for viewer modes.
package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const MaxReadSize = 256 * 1024

var (
	ErrInvalidOffset = errors.New("viewer: invalid offset")
	ErrInvalidLength = errors.New("viewer: invalid read length")
	ErrNotRegular    = errors.New("viewer: not a regular file")
)

type Metadata struct {
	Name       string
	Path       string
	Size       int64
	ModifiedAt string
}

// FileSource is deliberately independent of local filesystem details so a
// future SFTP implementation can expose the same bounded range API.
type FileSource interface {
	Open(context.Context, string) (File, error)
}

type File interface {
	Metadata(context.Context) (Metadata, error)
	ReadAt(context.Context, int64, int) ([]byte, error)
	Close() error
}

type LocalFileSource struct{}

func (LocalFileSource) Open(ctx context.Context, path string) (File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Check the resolved target before opening. Opening a FIFO or device may
	// block, while Stat is enough to reject it. The post-open Stat below closes
	// the race between this check and Open.
	target, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read file metadata: %w", err)
	}
	if !target.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read file metadata: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrNotRegular
	}
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &localFile{file: f, path: path}, nil
}

type localFile struct {
	file *os.File
	path string
}

func (f *localFile) Metadata(ctx context.Context) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	info, err := f.file.Stat()
	if err != nil {
		return Metadata{}, fmt.Errorf("read file metadata: %w", err)
	}
	return Metadata{Name: info.Name(), Path: f.path, Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z07:00")}, nil
}

func (f *localFile) ReadAt(ctx context.Context, offset int64, length int) ([]byte, error) {
	if offset < 0 {
		return nil, ErrInvalidOffset
	}
	if length < 0 || length > MaxReadSize {
		return nil, ErrInvalidLength
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if length == 0 {
		return []byte{}, nil
	}
	data := make([]byte, length)
	n, err := f.file.ReadAt(data, offset)
	data = data[:n]
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read file range: %w", err)
	}
	return data, err
}

func (f *localFile) Close() error { return f.file.Close() }
