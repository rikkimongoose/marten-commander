package localfs

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/settings"
)

type SystemEditorLauncher struct{}

type editorCandidate struct {
	executable string
	arguments  []string
}

func launchEditor(config settings.EditorConfig, path string, automatic []editorCandidate) error {
	if err := settings.ValidateEditorConfig(config); err != nil {
		return err
	}
	var candidate editorCandidate
	if config.Mode == settings.EditorCustom {
		candidate = editorCandidate{config.Executable, append([]string(nil), config.Arguments...)}
	} else {
		for _, possible := range automatic {
			if executable, err := exec.LookPath(possible.executable); err == nil {
				candidate = editorCandidate{executable, append([]string(nil), possible.arguments...)}
				break
			}
		}
		if candidate.executable == "" {
			return settings.ErrEditorNotFound
		}
	}
	executable, err := exec.LookPath(candidate.executable)
	if err != nil {
		return fmt.Errorf("исполняемый файл %q недоступен: %w", candidate.executable, settings.ErrEditorNotFound)
	}
	arguments, err := editorArguments(candidate.arguments, path)
	if err != nil {
		return err
	}
	command := exec.Command(executable, arguments...)
	if err := command.Start(); err != nil {
		return fmt.Errorf("%s: %w", executable, err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", executable, err)
		}
		return nil
	case <-timer.C:
		return nil
	}
}

func editorArguments(template []string, path string) ([]string, error) {
	arguments := make([]string, 0, len(template)+1)
	found := false
	for _, argument := range template {
		if strings.Contains(argument, "{file}") {
			if argument != "{file}" || found {
				return nil, fmt.Errorf("некорректный шаблон {file}: %w", settings.ErrInvalidEditorConfig)
			}
			arguments = append(arguments, path)
			found = true
		} else {
			arguments = append(arguments, argument)
		}
	}
	if !found {
		arguments = append(arguments, path)
	}
	return arguments, nil
}
