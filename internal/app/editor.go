package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/settings"
)

type editorFilesystem interface {
	Lstat(context.Context, string) (core.FileEntry, error)
	Open(context.Context, string) (io.ReadCloser, error)
}

type editorLauncher interface {
	LaunchEditor(settings.EditorConfig, string) error
}

type EditorService struct {
	files       editorFilesystem
	launcher    editorLauncher
	config      settings.EditorConfig
	configError error
}

func NewEditorService(files editorFilesystem, launcher editorLauncher, config settings.EditorConfig, configError error) *EditorService {
	return &EditorService{files: files, launcher: launcher, config: config, configError: configError}
}

func (s *EditorService) Edit(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.files == nil || s.launcher == nil {
		return errors.New("редактор недоступен")
	}
	if s.configError != nil {
		return fmt.Errorf("настройка редактора: %w", s.configError)
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return fmt.Errorf("некорректный путь: %w", fs.ErrInvalid)
	}
	entry, err := s.files.Lstat(ctx, path)
	if err != nil {
		return fmt.Errorf("проверить файл: %w", err)
	}
	if entry.Kind != core.KindFile {
		return fmt.Errorf("редактировать %q: %w: объект не является обычным файлом", path, fs.ErrInvalid)
	}
	reader, err := s.files.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("проверить доступ к файлу: %w", err)
	}
	if err := reader.Close(); err != nil {
		return fmt.Errorf("закрыть проверяемый файл: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.launcher.LaunchEditor(s.config, path); err != nil {
		return fmt.Errorf("запустить редактор: %w", err)
	}
	return nil
}
