package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// CommandService is only the transport boundary. Real operation handlers will
// be added individually; there is no registry, task manager or pretend success.
type CommandService struct{}

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
	return &CommandError{"unknown_command", "Этот тип backend-команды пока не поддерживается."}
}

func validateCommandContext(c CommandContext) error {
	invalid := func() error {
		return &CommandError{"invalid_context", "Некорректный контекст панелей или адрес файла."}
	}
	if c.ActivePanel != "left" && c.ActivePanel != "right" {
		return invalid()
	}
	validLocation := func(location string) bool {
		path, err := base64.RawURLEncoding.Strict().DecodeString(location)
		return err == nil && encode(string(path)) == location && filepath.IsAbs(string(path)) && !strings.ContainsRune(string(path), 0)
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
		if !validLocation(*panel.DirectoryLocation) {
			return invalid()
		}
		for _, location := range panel.SelectedLocations {
			if !validLocation(location) {
				return invalid()
			}
		}
		if panel.FocusedLocation != nil && !validLocation(*panel.FocusedLocation) {
			return invalid()
		}
	}
	return nil
}
