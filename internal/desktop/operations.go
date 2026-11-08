package desktop

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/rikkimongoose/marten-commander/internal/operations"
)

const OperationUpdatedEvent = "operations:updated"

type operationManager interface {
	Get(string) (operations.Operation, error)
	List() []operations.Operation
	Cancel(string) (operations.Operation, error)
	Close()
}

// OperationService exposes manager snapshots to Wails. Creating and starting
// work stays behind Go command handlers, so the UI cannot invent a runner or
// bypass CommandDispatcher when F5/F6/F8 are implemented.
type OperationService struct {
	manager operationManager
}

func NewOperationService(manager operationManager) *OperationService {
	return &OperationService{manager: manager}
}

func (s *OperationService) GetOperation(ctx context.Context, id string) (operations.Operation, error) {
	if err := ctx.Err(); err != nil {
		return operations.Operation{}, err
	}
	operation, err := s.manager.Get(id)
	if errors.Is(err, operations.ErrNotFound) {
		return operations.Operation{}, &CommandError{"invalid_request", "Операция не найдена."}
	}
	return operation, err
}

func (s *OperationService) ListOperations(ctx context.Context) ([]operations.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.manager.List(), nil
}

func (s *OperationService) CancelOperation(ctx context.Context, id string) (operations.Operation, error) {
	if err := ctx.Err(); err != nil {
		return operations.Operation{}, err
	}
	operation, err := s.manager.Cancel(id)
	if errors.Is(err, operations.ErrNotFound) {
		return operations.Operation{}, &CommandError{"invalid_request", "Операция не найдена."}
	}
	return operation, err
}

func (s *OperationService) ServiceShutdown() error {
	s.manager.Close()
	return nil
}

// OperationContextSnapshot converts the already frozen transport context into
// an operation snapshot. Locations remain opaque so unusual native filenames
// retain the same reversible encoding used by the existing commands.
func OperationContextSnapshot(command CommandContext, params json.RawMessage) (operations.ContextSnapshot, error) {
	if err := validateCommandContext(command); err != nil {
		return operations.ContextSnapshot{}, err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(params, &values); err != nil || values == nil {
		return operations.ContextSnapshot{}, &CommandError{"invalid_params", "Параметры команды должны быть JSON-объектом."}
	}
	canonical, err := json.Marshal(values)
	if err != nil {
		return operations.ContextSnapshot{}, &CommandError{"invalid_params", "Не удалось зафиксировать параметры команды."}
	}
	source, target := command.sourceAndTarget()
	if source.DirectoryLocation == nil {
		return operations.ContextSnapshot{}, &CommandError{"unavailable", "Исходный каталог ещё не открыт."}
	}
	snapshot := operations.ContextSnapshot{
		ActivePanel: command.ActivePanel, Sources: append([]string(nil), source.effectiveSelection()...),
		SourceDirectory: *source.DirectoryLocation, ParametersJSON: string(canonical),
	}
	if target.DirectoryLocation != nil {
		value := *target.DirectoryLocation
		snapshot.DestinationDirectory = &value
	}
	return snapshot, nil
}
