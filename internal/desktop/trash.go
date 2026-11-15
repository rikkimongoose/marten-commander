package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type trashCommandService interface {
	Prepare(context.Context, []string) ([]core.TrashSource, error)
	Start(context.Context, operations.ContextSnapshot, []core.TrashSource) (operations.Operation, error)
}

type TrashEntry struct {
	Location string `json:"location"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Revision string `json:"revision"`
}
type TrashPreview struct {
	Directory string       `json:"directory"`
	Entries   []TrashEntry `json:"entries"`
}

func trashPaths(command CommandContext) (string, []string, error) {
	if err := validateCommandContext(command); err != nil {
		return "", nil, err
	}
	source, _ := command.sourceAndTarget()
	if source.DirectoryLocation == nil {
		return "", nil, &CommandError{"unavailable", "Откройте локальный каталог; удалённая корзина не поддерживается."}
	}
	directory, ok := decodeLocation(*source.DirectoryLocation)
	if !ok {
		return "", nil, &CommandError{"invalid_context", "Нужен локальный каталог."}
	}
	paths := []string{}
	seen := map[string]bool{}
	for _, location := range source.effectiveSelection() {
		path, ok := decodeLocation(location)
		if !ok || filepath.Clean(path) != path || filepath.Dir(path) != filepath.Clean(directory) || path == filepath.Dir(path) || seen[path] {
			return "", nil, &CommandError{"invalid_context", "Источник корзины не принадлежит открытому каталогу или является защищённым путём."}
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return "", nil, &CommandError{"unavailable", "Не выбраны объекты для корзины."}
	}
	return directory, paths, nil
}

// PrepareTrash reads identities before confirmation; it cannot mutate files.
func (s *CommandService) PrepareTrash(ctx context.Context, command CommandContext) (TrashPreview, error) {
	directory, paths, err := trashPaths(command)
	if err != nil {
		return TrashPreview{}, err
	}
	if s.trash == nil {
		return TrashPreview{}, &CommandError{"unavailable", "Системная корзина недоступна."}
	}
	sources, err := s.trash.Prepare(ctx, paths)
	if err != nil {
		return TrashPreview{}, &CommandError{"trash_failed", fmt.Sprintf("Подготовить перемещение в корзину: %v", err)}
	}
	preview := TrashPreview{Directory: display(directory), Entries: make([]TrashEntry, 0, len(sources))}
	for _, source := range sources {
		preview.Entries = append(preview.Entries, TrashEntry{encode(source.Path), display(source.Name), string(source.Kind), source.Revision})
	}
	return preview, nil
}

func (s *CommandService) executeTrash(ctx context.Context, request CommandRequest, params map[string]json.RawMessage) error {
	_, paths, err := trashPaths(request.Context)
	if err != nil {
		return err
	}
	var entries []TrashEntry
	var confirmed bool
	if len(params) != 2 || json.Unmarshal(params["confirmed"], &confirmed) != nil || !confirmed || json.Unmarshal(params["entries"], &entries) != nil || len(entries) != len(paths) {
		return &CommandError{"invalid_params", "Требуется подтверждённый список объектов для корзины."}
	}
	sources := make([]core.TrashSource, len(paths))
	for i, path := range paths {
		if entries[i].Location != encode(path) || entries[i].Revision == "" {
			return &CommandError{"invalid_params", "Подтверждение не соответствует снимку выбранных объектов."}
		}
		sources[i] = core.TrashSource{Path: path, Name: filepath.Base(path), Revision: entries[i].Revision}
	}
	if s.trash == nil {
		return &CommandError{"unavailable", "Системная корзина недоступна."}
	}
	snapshot, err := OperationContextSnapshot(request.Context, request.Params)
	if err != nil {
		return err
	}
	snapshot.DestinationDirectory = nil
	if _, err = s.trash.Start(ctx, snapshot, sources); err != nil {
		return &CommandError{"trash_failed", fmt.Sprintf("Начать перемещение в корзину: %v", err)}
	}
	return nil
}
