package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// PanelContext uses the same reversible native-path encoding as Directory and
// Entry. A nil directory means the panel has not loaded a directory yet.
type PanelContext struct {
	DirectoryLocation *string  `json:"directoryLocation"`
	SelectedLocations []string `json:"selectedLocations"`
	FocusedLocation   *string  `json:"focusedLocation"`
}

// effectiveSelection keeps the transport distinction between explicit row
// selection and the keyboard cursor while giving file commands one rule for
// choosing their operands.
func (p PanelContext) effectiveSelection() []string {
	if len(p.SelectedLocations) > 0 {
		return p.SelectedLocations
	}
	if p.FocusedLocation != nil {
		return []string{*p.FocusedLocation}
	}
	return []string{}
}

type CommandContext struct {
	ActivePanel string       `json:"activePanel"`
	Left        PanelContext `json:"left"`
	Right       PanelContext `json:"right"`
}

func (c CommandContext) sourceAndTarget() (PanelContext, PanelContext) {
	if c.ActivePanel == "right" {
		return c.Right, c.Left
	}
	return c.Left, c.Right
}

type CommandRequest struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Context CommandContext  `json:"context"`
	Params  json.RawMessage `json:"params"`
}

// Wails serializes exported fields into RuntimeError.cause.
type CommandError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *CommandError) Error() string { return e.Message }

type fileCommandService interface {
	Open(context.Context, string) error
	CreateDirectory(context.Context, string, string) error
}

// CommandService is the Wails transport boundary. It decodes opaque native
// locations and delegates supported use cases to the application layer.
type CommandService struct {
	files fileCommandService
}

func NewCommandService(files fileCommandService) *CommandService {
	return &CommandService{files: files}
}

func (s *CommandService) ExecuteCommand(ctx context.Context, request CommandRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.Type) == "" {
		return &CommandError{"invalid_request", "Не указаны идентификатор вызова или тип команды."}
	}
	if err := validateCommandContext(request.Context); err != nil {
		return err
	}
	// The envelope requires a JSON object. Each future supported type must decode
	// its own parameter struct (rejecting unknown fields) and validate it before
	// invoking an application service, which must recheck paths and permissions.
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request.Params, &params); err != nil || params == nil {
		return &CommandError{"invalid_params", "Параметры команды должны быть JSON-объектом."}
	}
	if request.Type == "file.open" {
		if len(params) != 1 {
			return &CommandError{"invalid_params", "Команда открытия требует только адрес файла."}
		}
		var location string
		if raw, ok := params["location"]; !ok || json.Unmarshal(raw, &location) != nil || location == "" {
			return &CommandError{"invalid_params", "Не указан корректный адрес файла."}
		}
		path, ok := decodeLocation(location)
		if !ok {
			return &CommandError{"invalid_params", "Некорректный адрес файла."}
		}
		if s.files == nil {
			return &CommandError{"unavailable", "Системное открытие файлов недоступно."}
		}
		if err := s.files.Open(ctx, path); err != nil {
			return &CommandError{"open_failed", fmt.Sprintf("Не удалось открыть файл %q: %v", display(path), err)}
		}
		return nil
	}
	if request.Type == "file.mkdir" {
		if len(params) != 1 {
			return &CommandError{"invalid_params", "Команда создания каталога требует только имя."}
		}
		var name string
		if raw, ok := params["name"]; !ok || json.Unmarshal(raw, &name) != nil ||
			strings.TrimSpace(name) == "" || name == "." || name == ".." || filepath.IsAbs(name) ||
			filepath.VolumeName(name) != "" || strings.ContainsRune(name, os.PathSeparator) ||
			strings.ContainsRune(name, 0) || filepath.Base(name) != name {
			return &CommandError{"invalid_params", "Не указано корректное имя каталога."}
		}
		source, _ := request.Context.sourceAndTarget()
		if source.DirectoryLocation == nil {
			return &CommandError{"unavailable", "Каталог назначения ещё не открыт."}
		}
		parent, ok := decodeLocation(*source.DirectoryLocation)
		if !ok {
			return &CommandError{"invalid_context", "Некорректный адрес каталога назначения."}
		}
		if s.files == nil {
			return &CommandError{"unavailable", "Создание каталогов недоступно."}
		}
		if err := s.files.CreateDirectory(ctx, parent, name); err != nil {
			switch {
			case errors.Is(err, fs.ErrInvalid):
				return &CommandError{"invalid_params", "Имя должно быть одним непустым компонентом пути, отличным от «.» и «..»."}
			case errors.Is(err, fs.ErrExist):
				return &CommandError{"mkdir_failed", fmt.Sprintf("Каталог %q уже существует.", name)}
			case errors.Is(err, fs.ErrNotExist):
				return &CommandError{"mkdir_failed", "Каталог назначения больше не существует."}
			case errors.Is(err, fs.ErrPermission):
				return &CommandError{"mkdir_failed", fmt.Sprintf("Недостаточно прав для создания каталога в %q.", display(parent))}
			case errors.Is(err, errors.ErrUnsupported):
				return &CommandError{"unavailable", "Эта файловая система не поддерживает создание каталогов."}
			default:
				return &CommandError{"mkdir_failed", fmt.Sprintf("Не удалось создать каталог %q: %v", name, err)}
			}
		}
		return nil
	}
	return &CommandError{"unknown_command", "Этот тип backend-команды пока не поддерживается."}
}

func decodeLocation(location string) (string, bool) {
	path, err := base64.RawURLEncoding.Strict().DecodeString(location)
	return string(path), err == nil && encode(string(path)) == location && filepath.IsAbs(string(path)) &&
		!strings.ContainsRune(string(path), 0)
}

func validateCommandContext(c CommandContext) error {
	invalid := func() error {
		return &CommandError{"invalid_context", "Некорректный контекст панелей или адрес файла."}
	}
	if c.ActivePanel != "left" && c.ActivePanel != "right" {
		return invalid()
	}
	for _, panel := range []PanelContext{c.Left, c.Right} {
		if panel.SelectedLocations == nil {
			return invalid()
		}
		if panel.DirectoryLocation == nil {
			if len(panel.SelectedLocations) != 0 || panel.FocusedLocation != nil {
				return invalid()
			}
			continue
		}
		if _, ok := decodeLocation(*panel.DirectoryLocation); !ok {
			return invalid()
		}
		for _, location := range panel.SelectedLocations {
			if _, ok := decodeLocation(location); !ok {
				return invalid()
			}
		}
		if panel.FocusedLocation != nil {
			if _, ok := decodeLocation(*panel.FocusedLocation); !ok {
				return invalid()
			}
		}
	}
	return nil
}
