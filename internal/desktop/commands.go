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

	"github.com/rikkimongoose/marten-commander/internal/operations"
)

// PanelContext uses the same reversible native-path encoding as Directory and
// Entry. A nil directory means the panel has not loaded a directory yet.
type PanelContext struct {
	DirectoryLocation *string  `json:"directoryLocation"`
	SelectedLocations []string `json:"selectedLocations"`
	FocusedLocation   *string  `json:"focusedLocation"`
	FocusedKind       *string  `json:"focusedKind,omitempty"`
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

type copyCommandService interface {
	Start(context.Context, operations.ContextSnapshot, []string, string) (operations.Operation, error)
}

type moveCommandService interface {
	Start(context.Context, operations.ContextSnapshot, []string, string) (operations.Operation, error)
}

type editorCommandService interface {
	Edit(context.Context, string) error
}

// CommandService is the Wails transport boundary. It decodes opaque native
// locations and delegates supported use cases to the application layer.
type CommandService struct {
	files  fileCommandService
	copies copyCommandService
	moves  moveCommandService
	editor editorCommandService
}

func NewCommandService(files fileCommandService, copies ...copyCommandService) *CommandService {
	service := &CommandService{files: files}
	if len(copies) > 0 {
		service.copies = copies[0]
	}
	return service
}

func (s *CommandService) withMoves(moves moveCommandService) *CommandService {
	s.moves = moves
	return s
}

func NewCommandServiceWithMove(files fileCommandService, copies copyCommandService, moves moveCommandService) *CommandService {
	return NewCommandService(files, copies).withMoves(moves)
}

func NewCommandServiceWithOperations(files fileCommandService, editor editorCommandService, copies copyCommandService, moves moveCommandService) *CommandService {
	service := NewCommandServiceWithMove(files, copies, moves)
	service.editor = editor
	return service
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
	if request.Type == "file.edit" {
		if len(params) != 1 {
			return &CommandError{"invalid_params", "Команда редактирования требует только адрес файла."}
		}
		var location string
		if raw, ok := params["location"]; !ok || json.Unmarshal(raw, &location) != nil || location == "" {
			return &CommandError{"invalid_params", "Не указан корректный адрес файла для редактирования."}
		}
		path, ok := decodeLocation(location)
		if !ok {
			return &CommandError{"invalid_params", "Некорректный адрес файла для редактирования."}
		}
		source, _ := request.Context.sourceAndTarget()
		if source.DirectoryLocation == nil || source.FocusedLocation == nil || *source.FocusedLocation != location {
			return &CommandError{"invalid_context", "Редактировать можно только файл под курсором активной панели."}
		}
		directory, ok := decodeLocation(*source.DirectoryLocation)
		if !ok || filepath.Dir(path) != filepath.Clean(directory) {
			return &CommandError{"invalid_context", "Файл под курсором не принадлежит открытому каталогу."}
		}
		if s.editor == nil {
			return &CommandError{"unavailable", "Внешний текстовый редактор недоступен."}
		}
		if err := s.editor.Edit(ctx, path); err != nil {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return &CommandError{"edit_failed", "Файл исчез перед запуском редактора."}
			case errors.Is(err, fs.ErrPermission):
				return &CommandError{"edit_failed", "Недостаточно прав для чтения файла или запуска редактора."}
			case errors.Is(err, fs.ErrInvalid):
				return &CommandError{"edit_failed", "Под курсором находится объект, который нельзя редактировать как обычный файл."}
			default:
				return &CommandError{"edit_failed", fmt.Sprintf("Не удалось запустить редактор для файла %q: %v", display(path), err)}
			}
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
	if request.Type == "file.copy" {
		if len(params) != 1 {
			return &CommandError{"invalid_params", "Команда копирования требует только каталог назначения."}
		}
		var destination string
		if raw, ok := params["destination"]; !ok || json.Unmarshal(raw, &destination) != nil ||
			!filepath.IsAbs(destination) || strings.ContainsRune(destination, 0) {
			return &CommandError{"invalid_params", "Укажите абсолютный локальный каталог назначения."}
		}
		sourcePanel, _ := request.Context.sourceAndTarget()
		if sourcePanel.DirectoryLocation == nil {
			return &CommandError{"unavailable", "Исходный каталог ещё не открыт."}
		}
		sourceDirectory, ok := decodeLocation(*sourcePanel.DirectoryLocation)
		if !ok {
			return &CommandError{"invalid_context", "Некорректный адрес исходного каталога."}
		}
		locations := sourcePanel.effectiveSelection()
		sources := make([]string, 0, len(locations))
		for _, location := range locations {
			path, valid := decodeLocation(location)
			if !valid || filepath.Dir(path) != filepath.Clean(sourceDirectory) || filepath.Base(path) == "." || filepath.Base(path) == ".." {
				return &CommandError{"invalid_context", "Источник копирования не принадлежит открытому каталогу."}
			}
			sources = append(sources, path)
		}
		if len(sources) == 0 {
			return &CommandError{"unavailable", "Не выбран файл или каталог для копирования."}
		}
		if s.copies == nil {
			return &CommandError{"unavailable", "Копирование файлов недоступно."}
		}
		snapshot, err := OperationContextSnapshot(request.Context, request.Params)
		if err != nil {
			return err
		}
		encodedDestination := encode(destination)
		snapshot.DestinationDirectory = &encodedDestination
		if _, err := s.copies.Start(ctx, snapshot, sources, destination); err != nil {
			switch {
			case errors.Is(err, fs.ErrInvalid):
				return &CommandError{"invalid_params", err.Error()}
			case errors.Is(err, fs.ErrNotExist):
				return &CommandError{"copy_failed", "Каталог назначения больше не существует."}
			case errors.Is(err, fs.ErrPermission):
				return &CommandError{"copy_failed", fmt.Sprintf("Недостаточно прав для копирования в %q.", display(destination))}
			default:
				return &CommandError{"copy_failed", fmt.Sprintf("Не удалось начать копирование: %v", err)}
			}
		}
		return nil
	}
	if request.Type == "file.move" {
		if len(params) != 1 {
			return &CommandError{"invalid_params", "Команда перемещения требует только путь назначения."}
		}
		var destination string
		if raw, ok := params["destination"]; !ok || json.Unmarshal(raw, &destination) != nil ||
			strings.TrimSpace(destination) == "" || strings.ContainsRune(destination, 0) {
			return &CommandError{"invalid_params", "Укажите путь назначения или новое имя."}
		}
		sourcePanel, _ := request.Context.sourceAndTarget()
		if sourcePanel.DirectoryLocation == nil {
			return &CommandError{"unavailable", "Исходный каталог ещё не открыт."}
		}
		sourceDirectory, ok := decodeLocation(*sourcePanel.DirectoryLocation)
		if !ok {
			return &CommandError{"invalid_context", "Некорректный адрес исходного каталога."}
		}
		locations := sourcePanel.effectiveSelection()
		sources := make([]string, 0, len(locations))
		for _, location := range locations {
			path, valid := decodeLocation(location)
			if !valid || filepath.Dir(path) != filepath.Clean(sourceDirectory) || filepath.Base(path) == "." || filepath.Base(path) == ".." {
				return &CommandError{"invalid_context", "Источник перемещения не принадлежит открытому каталогу."}
			}
			sources = append(sources, path)
		}
		if len(sources) == 0 {
			return &CommandError{"unavailable", "Не выбран файл или каталог для перемещения."}
		}
		if !filepath.IsAbs(destination) {
			if len(sources) != 1 || destination == "." || destination == ".." || filepath.VolumeName(destination) != "" ||
				strings.ContainsRune(destination, os.PathSeparator) || filepath.Base(destination) != destination {
				return &CommandError{"invalid_params", "Для одного объекта укажите одно новое имя без пути; для нескольких — абсолютный путь существующего каталога."}
			}
			destination = filepath.Join(sourceDirectory, destination)
		} else {
			destination = filepath.Clean(destination)
		}
		if s.moves == nil {
			return &CommandError{"unavailable", "Перемещение файлов недоступно."}
		}
		normalizedParams, err := json.Marshal(struct {
			Destination string `json:"destination"`
		}{Destination: destination})
		if err != nil {
			return &CommandError{"invalid_params", "Не удалось зафиксировать путь назначения."}
		}
		snapshot, err := OperationContextSnapshot(request.Context, normalizedParams)
		if err != nil {
			return err
		}
		parent := destination
		if len(sources) == 1 {
			parent = filepath.Dir(destination)
		}
		encodedDestination := encode(parent)
		snapshot.DestinationDirectory = &encodedDestination
		if _, err := s.moves.Start(ctx, snapshot, sources, destination); err != nil {
			switch {
			case errors.Is(err, fs.ErrInvalid):
				return &CommandError{"invalid_params", err.Error()}
			case errors.Is(err, fs.ErrNotExist):
				return &CommandError{"move_failed", "Источник или каталог назначения больше не существует."}
			case errors.Is(err, fs.ErrPermission):
				return &CommandError{"move_failed", "Недостаточно прав для перемещения."}
			default:
				return &CommandError{"move_failed", fmt.Sprintf("Не удалось начать перемещение: %v", err)}
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
		if panel.FocusedKind != nil {
			if panel.FocusedLocation == nil || (*panel.FocusedKind != "file" && *panel.FocusedKind != "directory" && *panel.FocusedKind != "symlink" && *panel.FocusedKind != "other") {
				return invalid()
			}
		}
	}
	return nil
}
