package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type EditorMode string

const (
	EditorAuto   EditorMode = "auto"
	EditorCustom EditorMode = "custom"
)

var (
	ErrEditorNotFound      = errors.New("graphical text editor not found")
	ErrInvalidEditorConfig = errors.New("invalid editor configuration")
)

// EditorConfig is deliberately small until the application gets a settings
// screen. Arguments are already tokenised and never interpreted by a shell.
type EditorConfig struct {
	Mode       EditorMode
	Executable string
	Arguments  []string
}

func LoadEditorConfig() (EditorConfig, error) {
	mode := EditorMode(strings.TrimSpace(os.Getenv("MARTEN_COMMANDER_EDITOR_MODE")))
	if mode == "" {
		mode = EditorAuto
	}
	config := EditorConfig{Mode: mode, Executable: strings.TrimSpace(os.Getenv("MARTEN_COMMANDER_EDITOR_EXECUTABLE"))}
	if raw := strings.TrimSpace(os.Getenv("MARTEN_COMMANDER_EDITOR_ARGUMENTS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &config.Arguments); err != nil || config.Arguments == nil {
			return config, fmt.Errorf("MARTEN_COMMANDER_EDITOR_ARGUMENTS должен быть JSON-массивом строк: %w", ErrInvalidEditorConfig)
		}
	}
	if err := ValidateEditorConfig(config); err != nil {
		return config, err
	}
	return config, nil
}

func ValidateEditorConfig(config EditorConfig) error {
	if config.Mode != EditorAuto && config.Mode != EditorCustom {
		return fmt.Errorf("неизвестный режим редактора %q: %w", config.Mode, ErrInvalidEditorConfig)
	}
	if config.Mode == EditorAuto {
		return nil
	}
	if strings.TrimSpace(config.Executable) == "" || strings.ContainsRune(config.Executable, 0) {
		return fmt.Errorf("для режима custom укажите исполняемый файл: %w", ErrInvalidEditorConfig)
	}
	placeholders := 0
	for _, argument := range config.Arguments {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("аргумент редактора содержит NUL: %w", ErrInvalidEditorConfig)
		}
		if strings.Contains(argument, "{file}") {
			if argument != "{file}" {
				return fmt.Errorf("{file} должен быть отдельным аргументом: %w", ErrInvalidEditorConfig)
			}
			placeholders++
		}
	}
	if placeholders > 1 {
		return fmt.Errorf("{file} указан несколько раз: %w", ErrInvalidEditorConfig)
	}
	return nil
}
