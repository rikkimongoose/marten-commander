package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

const copyBufferSize = 128 * 1024

type copyFilesystem interface {
	core.FileSystem
	core.CopySource
	core.LinkReader
	core.PathResolver
	core.CopyDestination
}

type operationStarter interface {
	Create(string, operations.ContextSnapshot) (operations.Operation, error)
	Start(string, operations.Runner) (operations.Operation, error)
}

// CopyService starts immutable copy jobs in the shared OperationManager.
type CopyService struct {
	filesystem copyFilesystem
	operations operationStarter
}

func NewCopyService(filesystem copyFilesystem, manager operationStarter) *CopyService {
	return &CopyService{filesystem: filesystem, operations: manager}
}

func (s *CopyService) Start(ctx context.Context, snapshot operations.ContextSnapshot, sources []string, destination string) (operations.Operation, error) {
	if err := ctx.Err(); err != nil {
		return operations.Operation{}, err
	}
	if s == nil || s.filesystem == nil || s.operations == nil {
		return operations.Operation{}, errors.New("копирование недоступно")
	}
	if !filepath.IsAbs(destination) || strings.ContainsRune(destination, 0) {
		return operations.Operation{}, fmt.Errorf("каталог назначения должен быть абсолютным локальным путём: %w", fs.ErrInvalid)
	}
	unique := make([]string, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if !filepath.IsAbs(source) || strings.ContainsRune(source, 0) {
			return operations.Operation{}, fmt.Errorf("некорректный путь источника: %w", fs.ErrInvalid)
		}
		key := filepath.Clean(source)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, source)
	}
	if len(unique) == 0 {
		return operations.Operation{}, fmt.Errorf("не выбраны файлы для копирования: %w", fs.ErrInvalid)
	}
	destinationInfo, err := s.filesystem.Lstat(ctx, destination)
	if err != nil {
		return operations.Operation{}, fmt.Errorf("проверить каталог назначения: %w", err)
	}
	if destinationInfo.Kind == core.KindSymlink {
		resolved, resolveErr := s.filesystem.ResolvePath(ctx, destination)
		if resolveErr != nil {
			return operations.Operation{}, fmt.Errorf("разрешить каталог назначения: %w", resolveErr)
		}
		destination = resolved
		destinationInfo, err = s.filesystem.Lstat(ctx, destination)
		if err != nil {
			return operations.Operation{}, fmt.Errorf("проверить каталог назначения: %w", err)
		}
	}
	if destinationInfo.Kind != core.KindDirectory {
		return operations.Operation{}, fmt.Errorf("назначение не является каталогом: %w", fs.ErrInvalid)
	}
	probe, err := s.filesystem.CreateTemporaryFile(ctx, destination, 0o600)
	if err != nil {
		return operations.Operation{}, fmt.Errorf("проверить возможность записи в каталог назначения: %w", err)
	}
	probeName := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = s.filesystem.RemoveTemporary(context.WithoutCancel(ctx), probeName)
		return operations.Operation{}, fmt.Errorf("проверить каталог назначения: %w", closeErr)
	}
	if err := s.filesystem.RemoveTemporary(context.WithoutCancel(ctx), probeName); err != nil {
		return operations.Operation{}, fmt.Errorf("очистить проверочный файл назначения: %w", err)
	}
	created, err := s.operations.Create("file.copy", snapshot)
	if err != nil {
		return operations.Operation{}, err
	}
	return s.operations.Start(created.ID, &copyRunner{filesystem: s.filesystem, sources: unique, destination: destination})
}

type copyRunner struct {
	filesystem  copyFilesystem
	sources     []string
	destination string
	progress    operations.Progress
	copied      uint64
	skipped     uint64
}

func (r *copyRunner) Run(ctx context.Context, reporter operations.Reporter) error {
	for _, source := range r.sources {
		info, err := r.filesystem.Lstat(ctx, source)
		if err != nil {
			return r.failure(fmt.Errorf("источник %q недоступен: %w", source, err))
		}
		target := filepath.Join(r.destination, filepath.Base(source))
		if err := r.rejectSameEntry(ctx, source, target); err != nil {
			return r.failure(err)
		}
		if info.Kind == core.KindDirectory {
			if err := r.rejectSelfCopy(ctx, source, target); err != nil {
				return r.failure(err)
			}
		}
	}
	totalFiles, totalBytes, err := r.measure(ctx)
	if err != nil {
		return r.failure(err)
	}
	r.progress.TotalFiles, r.progress.TotalBytes = &totalFiles, &totalBytes
	if err := reporter.Report(r.progress); err != nil {
		return err
	}
	for _, source := range r.sources {
		info, err := r.filesystem.Lstat(ctx, source)
		if err != nil {
			return r.failure(fmt.Errorf("источник %q недоступен: %w", source, err))
		}
		destination := filepath.Join(r.destination, filepath.Base(source))
		if err := r.copyEntry(ctx, reporter, info, destination); err != nil {
			return r.failure(err)
		}
	}
	r.progress.CurrentFile = ""
	if err := reporter.Report(r.progress); err != nil {
		return err
	}
	return nil
}

func (r *copyRunner) rejectSameEntry(ctx context.Context, source, destination string) error {
	if filepath.Clean(source) == filepath.Clean(destination) {
		return fmt.Errorf("источник и назначение совпадают: %q", source)
	}
	sourceIdentity, err := r.filesystem.Fingerprint(ctx, source)
	if err != nil {
		return fmt.Errorf("проверить источник %q: %w", source, err)
	}
	destinationIdentity, err := r.filesystem.Fingerprint(ctx, destination)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("проверить назначение %q: %w", destination, err)
	}
	if sourceIdentity.Identity == destinationIdentity.Identity {
		return fmt.Errorf("источник и назначение указывают на один объект: %q", source)
	}
	return nil
}

func (r *copyRunner) failure(err error) error {
	if err == nil {
		return nil
	}
	code, message := "copy_failed", fmt.Sprintf("Копирование завершилось ошибкой: %v", err)
	if r.copied > 0 {
		code = "copy_partial"
		message = fmt.Sprintf("Копирование выполнено частично (%d объектов), затем произошла ошибка: %v", r.copied, err)
	}
	if errors.Is(err, context.Canceled) {
		code = "copy_cancelled"
		message = fmt.Sprintf("Копирование отменено после %d объектов.", r.copied)
	}
	return &operations.RunError{Code: code, Message: message, Cause: err}
}

func (r *copyRunner) measure(ctx context.Context) (uint64, uint64, error) {
	var files, bytes uint64
	var walk func(string) error
	walk = func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := r.filesystem.Lstat(ctx, path)
		if err != nil {
			return err
		}
		switch entry.Kind {
		case core.KindFile:
			files++
			if entry.Size > 0 {
				bytes += uint64(entry.Size)
			}
		case core.KindSymlink:
			files++
		case core.KindDirectory:
			children, err := r.filesystem.ReadCopyDirectory(ctx, path)
			if err != nil {
				return err
			}
			for _, child := range children {
				if err := walk(child.Path); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("специальный объект %q копировать нельзя", path)
		}
		return nil
	}
	for _, source := range r.sources {
		if err := walk(source); err != nil {
			return 0, 0, err
		}
	}
	return files, bytes, nil
}

func (r *copyRunner) rejectSelfCopy(ctx context.Context, source, destination string) error {
	resolvedSource, err := r.filesystem.ResolvePath(ctx, source)
	if err != nil {
		return fmt.Errorf("проверить исходный каталог: %w", err)
	}
	resolvedBase, err := r.filesystem.ResolvePath(ctx, filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("проверить каталог назначения: %w", err)
	}
	resolvedDestination := filepath.Join(resolvedBase, filepath.Base(destination))
	if _, statErr := r.filesystem.Lstat(ctx, destination); statErr == nil {
		if resolved, resolveErr := r.filesystem.ResolvePath(ctx, destination); resolveErr == nil {
			resolvedDestination = resolved
		}
	}
	relative, err := filepath.Rel(resolvedSource, resolvedDestination)
	if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("нельзя копировать каталог %q внутрь самого себя", source)
	}
	return nil
}

func (r *copyRunner) copyEntry(ctx context.Context, reporter operations.Reporter, source core.FileEntry, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch source.Kind {
	case core.KindDirectory:
		return r.copyDirectory(ctx, reporter, source, destination)
	case core.KindFile, core.KindSymlink:
		return r.copyLeaf(ctx, reporter, source, destination)
	default:
		return fmt.Errorf("специальный объект %q копировать нельзя", source.Path)
	}
}

func (r *copyRunner) copyDirectory(ctx context.Context, reporter operations.Reporter, source core.FileEntry, destination string) error {
	action, _, err := r.destinationAction(ctx, reporter, source, destination)
	if action == operations.Skip {
		r.skipped++
	}
	if err != nil || action == operations.Skip {
		return err
	}
	if action == "new" {
		if err := r.filesystem.CreateDirectoryPath(ctx, destination, source.Mode); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("создать каталог %q: %w", destination, err)
			}
			action, _, err = r.destinationAction(ctx, reporter, source, destination)
			if err != nil || action == operations.Skip {
				return err
			}
		} else {
			r.copied++
			r.progress.CurrentFile = source.Path
			if err := reporter.Report(r.progress); err != nil {
				return err
			}
		}
	}
	if action != "new" && action != operations.Merge {
		return fmt.Errorf("неподдерживаемое решение для каталога: %s", action)
	}
	children, err := r.filesystem.ReadCopyDirectory(ctx, source.Path)
	if err != nil {
		return fmt.Errorf("прочитать каталог %q: %w", source.Path, err)
	}
	for _, child := range children {
		if err := r.copyEntry(ctx, reporter, child, filepath.Join(destination, child.Name)); err != nil {
			return err
		}
	}
	return nil
}

func (r *copyRunner) copyLeaf(ctx context.Context, reporter operations.Reporter, source core.FileEntry, destination string) (err error) {
	keepBothBase := destination
	action, fingerprint, err := r.destinationAction(ctx, reporter, source, destination)
	if action == operations.Skip {
		r.skipped++
	}
	if err != nil || action == operations.Skip {
		return err
	}
	return r.copyLeafResolved(ctx, reporter, source, destination, keepBothBase, action, fingerprint)
}

func (r *copyRunner) copyLeafResolved(ctx context.Context, reporter operations.Reporter, source core.FileEntry, destination, keepBothBase string, action operations.ConflictAction, fingerprint core.FileFingerprint) (err error) {
	if action == operations.KeepBoth {
		destination, err = operations.NextKeepBothPath(keepBothBase, func(candidate string) (bool, error) {
			_, statErr := r.filesystem.Lstat(ctx, candidate)
			return statErr == nil, ignoreNotExist(statErr)
		})
		if err != nil {
			return err
		}
		action = "new"
	}
	var temporary string
	if source.Kind == core.KindSymlink {
		target, readErr := r.filesystem.Readlink(ctx, source.Path)
		if readErr != nil {
			return fmt.Errorf("прочитать ссылку %q: %w", source.Path, readErr)
		}
		temporary, err = r.filesystem.CreateTemporarySymlink(ctx, filepath.Dir(destination), target)
	} else {
		temporary, err = r.copyRegularToTemporary(ctx, reporter, source, filepath.Dir(destination))
	}
	if err != nil {
		return err
	}
	defer func() {
		if temporary != "" {
			err = errors.Join(err, r.filesystem.RemoveTemporary(context.WithoutCancel(ctx), temporary))
		}
	}()

	for {
		switch action {
		case "new":
			err = r.filesystem.PublishNew(ctx, temporary, destination)
			if errors.Is(err, fs.ErrExist) {
				action, fingerprint, err = r.destinationAction(ctx, reporter, source, destination)
				if err != nil || action == operations.Skip {
					return err
				}
				if action == operations.KeepBoth {
					destination, err = operations.NextKeepBothPath(keepBothBase, func(candidate string) (bool, error) {
						_, statErr := r.filesystem.Lstat(ctx, candidate)
						return statErr == nil, ignoreNotExist(statErr)
					})
					if err != nil {
						return err
					}
					action = "new"
				}
				continue
			}
		case operations.Replace:
			err = r.filesystem.PublishReplace(ctx, temporary, destination, fingerprint)
		default:
			return fmt.Errorf("неподдерживаемое решение для файла: %s", action)
		}
		if err != nil {
			if errors.Is(err, core.ErrTemporaryHoldsDestination) {
				recovery := temporary
				temporary = ""
				return fmt.Errorf("не удалось восстановить конкурентно изменённое назначение; исходный объект сохранён в %q: %w", recovery, err)
			}
			if errors.Is(err, core.ErrDestinationChanged) {
				return fmt.Errorf("назначение %q изменилось после подтверждения; замена безопасно отменена: %w", destination, err)
			}
			return fmt.Errorf("опубликовать %q: %w", destination, err)
		}
		temporary = ""
		r.copied++
		r.progress.ProcessedFiles++
		r.progress.CurrentFile = source.Path
		return reporter.Report(r.progress)
	}
}

func (r *copyRunner) copyRegularToTemporary(ctx context.Context, reporter operations.Reporter, source core.FileEntry, directory string) (result string, err error) {
	input, err := r.filesystem.OpenCopyFile(ctx, source.Path)
	if err != nil {
		return "", fmt.Errorf("открыть %q: %w", source.Path, err)
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	output, err := r.filesystem.CreateTemporaryFile(ctx, directory, source.Mode)
	if err != nil {
		return "", fmt.Errorf("создать временный файл: %w", err)
	}
	temporary := output.Name()
	ok := false
	defer func() {
		if !ok {
			_ = output.Close()
			_ = r.filesystem.RemoveTemporary(context.WithoutCancel(ctx), temporary)
		}
	}()
	buffer := make([]byte, copyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			written, writeErr := output.Write(buffer[:count])
			if writeErr != nil {
				return "", fmt.Errorf("записать временный файл: %w", writeErr)
			}
			if written != count {
				return "", io.ErrShortWrite
			}
			r.progress.ProcessedBytes += uint64(written)
			r.progress.CurrentFile = source.Path
			if err := reporter.Report(r.progress); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("прочитать %q: %w", source.Path, readErr)
		}
	}
	if err := output.Sync(); err != nil {
		return "", fmt.Errorf("синхронизировать временный файл: %w", err)
	}
	if err := output.Close(); err != nil {
		return "", fmt.Errorf("закрыть временный файл: %w", err)
	}
	ok = true
	return temporary, nil
}

func (r *copyRunner) destinationAction(ctx context.Context, reporter operations.Reporter, source core.FileEntry, destination string) (operations.ConflictAction, core.FileFingerprint, error) {
	destinationInfo, err := r.filesystem.Lstat(ctx, destination)
	if errors.Is(err, fs.ErrNotExist) {
		return "new", core.FileFingerprint{}, nil
	}
	if err != nil {
		return "", core.FileFingerprint{}, fmt.Errorf("проверить назначение %q: %w", destination, err)
	}
	if destinationInfo.Kind == core.KindOther {
		return "", core.FileFingerprint{}, fmt.Errorf("назначение %q является специальным объектом", destination)
	}
	typeOf := operations.FileFile
	if source.Kind == core.KindDirectory && destinationInfo.Kind == core.KindDirectory {
		typeOf = operations.DirectoryDirectory
	} else if source.Kind == core.KindDirectory {
		typeOf = operations.DirectoryFile
	} else if destinationInfo.Kind == core.KindDirectory {
		typeOf = operations.FileDirectory
	}
	fingerprint, err := r.filesystem.Fingerprint(ctx, destination)
	if err != nil {
		return "", core.FileFingerprint{}, fmt.Errorf("зафиксировать назначение %q: %w", destination, err)
	}
	decision, err := reporter.RequestConflict(operations.ConflictRequest{Type: typeOf,
		Source: conflictMetadata(source), Destination: conflictMetadata(destinationInfo)})
	if err != nil {
		return "", core.FileFingerprint{}, err
	}
	return decision.Resolution.Action, fingerprint, nil
}

func conflictMetadata(entry core.FileEntry) operations.ConflictMetadata {
	kind := operations.File
	if entry.Kind == core.KindDirectory {
		kind = operations.Directory
	}
	metadata := operations.ConflictMetadata{Path: entry.Path, Kind: kind, ModifiedAt: &entry.ModifiedAt}
	if entry.Kind == core.KindFile {
		size := uint64(max(entry.Size, 0))
		metadata.Size = &size
	}
	return metadata
}

func ignoreNotExist(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
