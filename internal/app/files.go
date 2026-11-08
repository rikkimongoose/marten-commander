package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

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
	files       fileInfoReader
	launcher    fileLauncher
	directories core.DirectoryCreator
}

func NewFileService(files fileInfoReader, launcher fileLauncher) *FileService {
	directories, _ := files.(core.DirectoryCreator)
	return &FileService{files: files, launcher: launcher, directories: directories}
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

// CreateDirectory validates the command as a single local path component and
// delegates the actual one-level mutation to the filesystem capability.
func (s *FileService) CreateDirectory(ctx context.Context, parent, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" || name == "." || name == ".." ||
		filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.ContainsRune(name, filepath.Separator) ||
		strings.ContainsRune(name, 0) || filepath.Base(name) != name {
		return fmt.Errorf("имя должно быть одним компонентом пути: %w", fs.ErrInvalid)
	}
	if s.directories == nil {
		return fmt.Errorf("создание каталогов не поддерживается: %w", errors.ErrUnsupported)
	}
	if err := s.directories.CreateDirectory(ctx, parent, name); err != nil {
		return fmt.Errorf("создать каталог: %w", err)
	}
	return nil
}
