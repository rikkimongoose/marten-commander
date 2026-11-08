package app

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

type fileInfoReader interface {
	Lstat(context.Context, string) (core.FileEntry, error)
}

type fileLauncher interface {
	OpenFile(string) error
}

// FileService validates a concrete filesystem object before handing it to the
// host association mechanism. It never interprets the path as a command.
type FileService struct {
	files    fileInfoReader
	launcher fileLauncher
}

func NewFileService(files fileInfoReader, launcher fileLauncher) *FileService {
	return &FileService{files: files, launcher: launcher}
}

func (s *FileService) Open(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry, err := s.files.Lstat(ctx, path)
	if err != nil {
		return fmt.Errorf("проверить файл: %w", err)
	}
	if entry.Kind != core.KindFile {
		return fmt.Errorf("открыть %q: %w: объект не является обычным файлом", path, fs.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.launcher.OpenFile(path); err != nil {
		return fmt.Errorf("запустить системное приложение: %w", err)
	}
	return nil
}
