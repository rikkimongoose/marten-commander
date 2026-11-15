package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rikkimongoose/marten-commander/internal/settings"
)

// TerminalLauncher is the platform boundary; Windows can supply its own adapter.
type TerminalLauncher interface {
	LaunchTerminal(settings.TerminalConfig, string, string) error
}

type TerminalService struct {
	launcher    TerminalLauncher
	config      settings.TerminalConfig
	configError error
}

func NewTerminalService(launcher TerminalLauncher, config settings.TerminalConfig, configError error) *TerminalService {
	config.Arguments = append([]string(nil), config.Arguments...)
	config.ShellArguments = append([]string(nil), config.ShellArguments...)
	return &TerminalService{launcher, config, configError}
}

func (s *TerminalService) Run(ctx context.Context, command, directory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(command) == "" {
		return nil
	}
	if s.configError != nil {
		return s.configError
	}
	if strings.ContainsRune(command, 0) || strings.ContainsRune(directory, 0) || !filepath.IsAbs(directory) {
		return fmt.Errorf("нужны корректная команда и абсолютный локальный каталог")
	}
	f, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("рабочий каталог: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("рабочий путь не является каталогом")
	}
	if s.launcher == nil {
		return fmt.Errorf("запуск терминала недоступен")
	}
	return s.launcher.LaunchTerminal(s.config, command, directory)
}
