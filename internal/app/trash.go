package app

import (
	"context"
	"fmt"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type TrashService struct {
	backend    core.TrashBackend
	operations operationStarter
}

func NewTrashService(backend core.TrashBackend, manager operationStarter) *TrashService {
	return &TrashService{backend, manager}
}

// Prepare is read-only and happens before the confirmation dialog.
func (s *TrashService) Prepare(ctx context.Context, paths []string) ([]core.TrashSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("системная корзина недоступна")
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("не выбраны объекты для корзины")
	}
	sources := make([]core.TrashSource, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			return nil, fmt.Errorf("источник указан несколько раз")
		}
		seen[path] = true
		source, err := s.backend.InspectTrash(ctx, path)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func (s *TrashService) Start(ctx context.Context, snapshot operations.ContextSnapshot, sources []core.TrashSource) (operations.Operation, error) {
	if s == nil || s.backend == nil || s.operations == nil {
		return operations.Operation{}, fmt.Errorf("системная корзина недоступна")
	}
	if err := ctx.Err(); err != nil {
		return operations.Operation{}, err
	}
	if len(sources) == 0 {
		return operations.Operation{}, fmt.Errorf("не выбраны объекты для корзины")
	}
	for _, source := range sources {
		if source.Revision == "" {
			return operations.Operation{}, fmt.Errorf("не подтверждена идентичность источника")
		}
	}
	snapshot.DestinationDirectory = nil
	created, err := s.operations.Create("file.trash", snapshot)
	if err != nil {
		return operations.Operation{}, err
	}
	frozen := append([]core.TrashSource(nil), sources...)
	return s.operations.Start(created.ID, &trashRunner{backend: s.backend, sources: frozen})
}

type trashRunner struct {
	backend core.TrashBackend
	sources []core.TrashSource
}

func (r *trashRunner) Run(ctx context.Context, reporter operations.Reporter) error {
	total := uint64(len(r.sources))
	progress := operations.Progress{TotalFiles: &total}
	boundary, ok := reporter.(operations.ItemReporter)
	if !ok {
		return fmt.Errorf("учёт атомарного переноса в корзину недоступен")
	}
	for i, source := range r.sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress.CurrentFile = source.Path
		if err := reporter.Report(progress); err != nil {
			return err
		}
		err := boundary.Item(func() (operations.Progress, error) {
			err := r.backend.Trash(context.WithoutCancel(ctx), source)
			if err != nil {
				return progress, &operations.RunError{Code: "trash_failed", Message: fmt.Sprintf("В корзину перемещено %d из %d. Ошибка для %q: %v", progress.ProcessedFiles, total, source.Name, err), Cause: err}
			}
			progress.ProcessedFiles++
			return progress, nil
		}, i == len(r.sources)-1)
		if err != nil {
			return err
		}
	}
	return nil
}
