package archive

import (
	"context"
	"fmt"
	"os"
)

// FileInputOpener adapts an absolute local file path to ArchiveSource. It is
// intentionally small; remote and virtual providers can supply their own
// seek-independent Input implementations.
type FileInputOpener struct{}

func (FileInputOpener) OpenArchive(ctx context.Context, location string) (Input, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	file, err := os.Open(location)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("archive input %q is not a regular file", location)
	}
	return &fileInput{File: file, size: info.Size()}, nil
}

type fileInput struct {
	*os.File
	size int64
}

func (f *fileInput) Size() int64 { return f.size }

// NewZIPSource constructs the normal ArchiveSource with ZIP registered.
func NewZIPSource(opener InputOpener) *Source {
	registry := NewRegistry()
	_ = RegisterZIP(registry)
	return NewSource(opener, registry)
}
