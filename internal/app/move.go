package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type moveFilesystem interface {
	copyFilesystem
	core.MoveFileSystem
}

type MoveService struct {
	filesystem moveFilesystem
	operations operationStarter
}

func NewMoveService(filesystem moveFilesystem, manager operationStarter) *MoveService {
	return &MoveService{filesystem: filesystem, operations: manager}
}

// Start resolves the user-entered target once. A single source may name a new
// final path; multiple sources always require an existing directory.
func (s *MoveService) Start(ctx context.Context, snapshot operations.ContextSnapshot, sources []string, destination string) (operations.Operation, error) {
	if err := ctx.Err(); err != nil {
		return operations.Operation{}, err
	}
	if s == nil || s.filesystem == nil || s.operations == nil {
		return operations.Operation{}, errors.New("перемещение недоступно")
	}
	if !filepath.IsAbs(destination) || strings.ContainsRune(destination, 0) {
		return operations.Operation{}, fmt.Errorf("назначение должно быть абсолютным локальным путём: %w", fs.ErrInvalid)
	}
	unique := make([]string, 0, len(sources))
	seen := map[string]struct{}{}
	for _, source := range sources {
		if !filepath.IsAbs(source) || strings.ContainsRune(source, 0) {
			return operations.Operation{}, fmt.Errorf("некорректный путь источника: %w", fs.ErrInvalid)
		}
		clean := filepath.Clean(source)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		unique = append(unique, clean)
	}
	if len(unique) == 0 {
		return operations.Operation{}, fmt.Errorf("не выбраны файлы для перемещения: %w", fs.ErrInvalid)
	}

	targets := make([]string, len(unique))
	destinationInfo, statErr := s.filesystem.Lstat(ctx, destination)
	if statErr == nil && destinationInfo.Kind == core.KindSymlink {
		resolved, err := s.filesystem.ResolvePath(ctx, destination)
		if err != nil {
			return operations.Operation{}, fmt.Errorf("разрешить назначение: %w", err)
		}
		destination = resolved
		destinationInfo, statErr = s.filesystem.Lstat(ctx, destination)
	}
	if len(unique) > 1 {
		if statErr != nil {
			return operations.Operation{}, fmt.Errorf("для нескольких источников назначение должно быть существующим каталогом: %w", statErr)
		}
		if destinationInfo.Kind != core.KindDirectory {
			return operations.Operation{}, fmt.Errorf("для нескольких источников назначение должно быть каталогом: %w", fs.ErrInvalid)
		}
		for i, source := range unique {
			targets[i] = filepath.Join(destination, filepath.Base(source))
		}
	} else if statErr == nil && destinationInfo.Kind == core.KindDirectory {
		targets[0] = filepath.Join(destination, filepath.Base(unique[0]))
	} else if statErr == nil || errors.Is(statErr, fs.ErrNotExist) {
		targets[0] = filepath.Clean(destination)
	} else {
		return operations.Operation{}, fmt.Errorf("проверить назначение: %w", statErr)
	}
	parent := filepath.Dir(targets[0])
	if len(unique) > 1 {
		parent = destination
	}
	parentInfo, err := s.filesystem.Lstat(ctx, parent)
	if err != nil {
		return operations.Operation{}, fmt.Errorf("проверить каталог назначения: %w", err)
	}
	if parentInfo.Kind != core.KindDirectory {
		return operations.Operation{}, fmt.Errorf("родитель назначения не является каталогом: %w", fs.ErrInvalid)
	}
	probe, err := s.filesystem.CreateTemporaryFile(ctx, parent, 0o600)
	if err != nil {
		return operations.Operation{}, fmt.Errorf("проверить возможность записи: %w", err)
	}
	probeName := probe.Name()
	closeErr := probe.Close()
	removeErr := s.filesystem.RemoveTemporary(context.WithoutCancel(ctx), probeName)
	if closeErr != nil || removeErr != nil {
		return operations.Operation{}, errors.Join(closeErr, removeErr)
	}

	created, err := s.operations.Create("file.move", snapshot)
	if err != nil {
		return operations.Operation{}, err
	}
	return s.operations.Start(created.ID, &moveRunner{filesystem: s.filesystem, sources: unique, targets: targets})
}

type moveRunner struct {
	filesystem       moveFilesystem
	sources, targets []string
	progress         operations.Progress
	moved            uint64
	copy             copyRunner
	completed        bool
	changed          bool
}

func (r *moveRunner) Run(ctx context.Context, reporter operations.Reporter) error {
	r.copy = copyRunner{filesystem: r.filesystem}
	var totalFiles, totalBytes uint64
	for _, source := range r.sources {
		files, bytes, err := measureEntry(ctx, r.filesystem, source)
		if err != nil {
			return r.failure(err)
		}
		totalFiles += files
		totalBytes += bytes
	}
	r.progress.TotalFiles, r.progress.TotalBytes = &totalFiles, &totalBytes
	r.copy.progress = r.progress
	if err := reporter.Report(r.progress); err != nil {
		return err
	}
	for i, source := range r.sources {
		info, err := r.filesystem.Lstat(ctx, source)
		if err != nil {
			return r.failure(fmt.Errorf("источник %q недоступен: %w", source, err))
		}
		if filepath.Clean(source) == filepath.Clean(r.targets[i]) {
			return r.failure(fmt.Errorf("источник и назначение совпадают: %q", source))
		}
		if info.Kind == core.KindDirectory {
			if err := r.copy.rejectSelfCopy(ctx, source, r.targets[i]); err != nil {
				return r.failure(errors.New(strings.Replace(err.Error(), "копировать", "перемещать", 1)))
			}
		}
		if err := r.copy.rejectSameEntry(ctx, source, r.targets[i]); err != nil {
			return r.failure(err)
		}
		if _, err := r.moveEntry(ctx, reporter, info, r.targets[i], i == len(r.sources)-1); err != nil {
			return r.failure(err)
		}
	}
	if r.completed {
		return nil
	}
	r.progress = r.copy.progress
	r.progress.CurrentFile = ""
	if err := reporter.Report(r.progress); err != nil && !errors.Is(err, operations.ErrInvalidTransition) {
		return err
	}
	return nil
}

func (r *moveRunner) moveEntry(ctx context.Context, reporter operations.Reporter, source core.FileEntry, destination string, final bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if source.Kind == core.KindOther {
		return false, fmt.Errorf("специальный объект %q перемещать нельзя", source.Path)
	}
	sourceFingerprint, err := r.filesystem.Fingerprint(ctx, source.Path)
	if err != nil {
		return false, err
	}
	action, destinationFingerprint, err := r.copy.destinationAction(ctx, reporter, source, destination)
	if err != nil {
		return false, err
	}
	if action == operations.Skip {
		r.copy.skipped++
		return false, nil
	}
	if action == operations.KeepBoth {
		destination, err = operations.NextKeepBothPath(destination, func(candidate string) (bool, error) {
			_, e := r.filesystem.Lstat(ctx, candidate)
			return e == nil, ignoreNotExist(e)
		})
		if err != nil {
			return false, err
		}
		action = "new"
	}
	if action == "new" || action == operations.Replace {
		var renameErr error
		atomicFiles, atomicBytes, measureErr := measureEntry(ctx, r.filesystem, source.Path)
		if measureErr != nil {
			return false, measureErr
		}
		rename := func() error {
			if action == "new" {
				return r.filesystem.RenameNew(ctx, source.Path, destination, sourceFingerprint)
			}
			if source.Kind != core.KindDirectory {
				return r.filesystem.RenameReplace(ctx, source.Path, destination, sourceFingerprint, destinationFingerprint)
			}
			return syscall.EXDEV
		}
		if atomic, ok := reporter.(operations.AtomicReporter); ok {
			next := r.copy.progress
			renameErr = atomic.Atomic(func() (operations.Progress, error) {
				if err := rename(); err != nil {
					return next, err
				}
				next.ProcessedFiles += atomicFiles
				next.ProcessedBytes += atomicBytes
				next.CurrentFile = source.Path
				return next, nil
			}, final)
			if renameErr == nil {
				r.copy.progress = next
				r.moved++
				r.completed = final
				return true, nil
			}
		} else {
			renameErr = rename()
		}
		if renameErr == nil && !(action == operations.Replace && source.Kind == core.KindDirectory) {
			files, bytes, _ := measureEntry(context.WithoutCancel(ctx), r.filesystem, destination)
			r.copy.progress.ProcessedFiles += files
			r.copy.progress.ProcessedBytes += bytes
			r.copy.progress.CurrentFile = source.Path
			r.moved++
			if err := reporter.Report(r.copy.progress); err != nil && ctx.Err() == nil {
				return true, err
			}
			return true, nil
		}
		if renameErr != nil && !errors.Is(renameErr, syscall.EXDEV) && !errors.Is(renameErr, errors.ErrUnsupported) && !errors.Is(renameErr, fs.ErrExist) {
			if errors.Is(renameErr, core.ErrTemporaryHoldsDestination) {
				r.changed = true
			}
			return false, fmt.Errorf("переименовать %q: %w", source.Path, renameErr)
		}
		if errors.Is(renameErr, fs.ErrExist) {
			return r.moveEntry(ctx, reporter, source, destination, final)
		}
	}
	if source.Kind == core.KindDirectory {
		if action != "new" && action != operations.Merge {
			return false, fmt.Errorf("неподдерживаемое решение для каталога: %s", action)
		}
		if action == "new" {
			if err := r.filesystem.CreateDirectoryPath(ctx, destination, source.Mode); err != nil && !errors.Is(err, fs.ErrExist) {
				return false, err
			}
		}
		children, err := r.filesystem.ReadCopyDirectory(ctx, source.Path)
		if err != nil {
			return false, err
		}
		allMoved := true
		for _, child := range children {
			moved, childErr := r.moveEntry(ctx, reporter, child, filepath.Join(destination, child.Name), false)
			if childErr != nil {
				return false, childErr
			}
			allMoved = allMoved && moved
		}
		if !allMoved {
			return false, nil
		}
		remaining, err := r.filesystem.ReadCopyDirectory(ctx, source.Path)
		if err != nil {
			return false, err
		}
		if len(remaining) != 0 {
			return false, nil
		}
		current, err := r.filesystem.Fingerprint(ctx, source.Path)
		if err != nil {
			return false, err
		}
		if err := r.removeSource(ctx, reporter, source.Path, current, final); err != nil {
			return false, fmt.Errorf("удалить пустой исходный каталог %q: %w", source.Path, err)
		}
		r.moved++
		return true, nil
	}
	before := r.copy.copied
	if err := r.copy.copyLeafResolved(ctx, reporter, source, destination, destination, action, destinationFingerprint); err != nil {
		return false, err
	}
	if r.copy.copied == before {
		return false, nil
	}
	current, err := r.filesystem.Fingerprint(ctx, source.Path)
	if err != nil {
		return false, fmt.Errorf("проверить источник перед удалением: %w", err)
	}
	if current != sourceFingerprint {
		return false, fmt.Errorf("исходный объект %q изменился после копирования: %w", source.Path, core.ErrSourceChanged)
	}
	if err := r.removeSource(ctx, reporter, source.Path, sourceFingerprint, final); err != nil {
		return false, fmt.Errorf("копия создана, но исходный объект %q не удалён: %w", source.Path, err)
	}
	r.moved++
	return true, nil
}

func (r *moveRunner) removeSource(ctx context.Context, reporter operations.Reporter, source string, expected core.FileFingerprint, final bool) error {
	if final {
		if atomic, ok := reporter.(operations.AtomicReporter); ok {
			err := atomic.Atomic(func() (operations.Progress, error) {
				return r.copy.progress, r.filesystem.RemoveSource(ctx, source, expected)
			}, true)
			if err == nil {
				r.completed = true
			}
			return err
		}
	}
	return r.filesystem.RemoveSource(ctx, source, expected)
}

func (r *moveRunner) failure(err error) error {
	if err == nil {
		return nil
	}
	code, message := "move_failed", fmt.Sprintf("Перемещение завершилось ошибкой: %v", err)
	if r.moved > 0 || r.copy.copied > 0 || r.changed {
		code, message = "move_partial", fmt.Sprintf("Перемещение выполнено частично, затем произошла ошибка: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		code, message = "move_cancelled", fmt.Sprintf("Перемещение отменено; перенесено объектов: %d.", r.moved)
	}
	return &operations.RunError{Code: code, Message: message, Cause: err}
}

func measureEntry(ctx context.Context, filesystem copyFilesystem, path string) (uint64, uint64, error) {
	runner := copyRunner{filesystem: filesystem, sources: []string{path}}
	return runner.measure(ctx)
}
