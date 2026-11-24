package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type remoteDownloadSource interface {
	Lstat(context.Context, string, string) (core.FileEntry, error)
	Open(context.Context, string, string) (io.ReadCloser, error)
}

type downloadDestination interface {
	core.FileSystem
	core.PathResolver
	core.CopyDestination
}

// DownloadService copies regular files from one captured SFTP session into a
// local directory. It deliberately exposes no remote mutation capability.
type DownloadService struct {
	remote     remoteDownloadSource
	local      downloadDestination
	operations operationStarter
}

func NewDownloadService(remote remoteDownloadSource, local downloadDestination, manager operationStarter) *DownloadService {
	return &DownloadService{remote: remote, local: local, operations: manager}
}

func (s *DownloadService) Start(ctx context.Context, snapshot operations.ContextSnapshot, connectionID string,
	sources []string, destination string) (operations.Operation, error) {
	if err := ctx.Err(); err != nil {
		return operations.Operation{}, err
	}
	if s == nil || s.remote == nil || s.local == nil || s.operations == nil || connectionID == "" {
		return operations.Operation{}, errors.New("скачивание недоступно")
	}
	if !filepath.IsAbs(destination) || strings.ContainsRune(destination, 0) {
		return operations.Operation{}, fmt.Errorf("каталог назначения должен быть абсолютным локальным путём: %w", fs.ErrInvalid)
	}
	destinationInfo, err := s.local.Lstat(ctx, destination)
	if err != nil {
		return operations.Operation{}, fmt.Errorf("проверить каталог назначения: %w", err)
	}
	if destinationInfo.Kind == core.KindSymlink {
		destination, err = s.local.ResolvePath(ctx, destination)
		if err != nil {
			return operations.Operation{}, fmt.Errorf("разрешить каталог назначения: %w", err)
		}
		destinationInfo, err = s.local.Lstat(ctx, destination)
		if err != nil {
			return operations.Operation{}, fmt.Errorf("проверить каталог назначения: %w", err)
		}
	}
	if destinationInfo.Kind != core.KindDirectory {
		return operations.Operation{}, fmt.Errorf("назначение не является каталогом: %w", fs.ErrInvalid)
	}
	unique := make([]string, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if !path.IsAbs(source) || strings.ContainsRune(source, 0) {
			return operations.Operation{}, fmt.Errorf("некорректный SFTP-путь: %w", fs.ErrInvalid)
		}
		source = path.Clean(source)
		if _, exists := seen[source]; exists {
			continue
		}
		entry, statErr := s.remote.Lstat(ctx, connectionID, source)
		if statErr != nil {
			return operations.Operation{}, fmt.Errorf("проверить SFTP-файл %q: %w", source, statErr)
		}
		if entry.Kind != core.KindFile {
			return operations.Operation{}, fmt.Errorf("скачивать можно только обычные файлы; каталоги и символические ссылки не поддерживаются: %q: %w", source, fs.ErrInvalid)
		}
		seen[source] = struct{}{}
		unique = append(unique, source)
	}
	if len(unique) == 0 {
		return operations.Operation{}, fmt.Errorf("не выбраны файлы: %w", fs.ErrInvalid)
	}
	probe, err := s.local.CreateTemporaryFile(ctx, destination, 0o600)
	if err != nil {
		return operations.Operation{}, fmt.Errorf("проверить возможность записи: %w", err)
	}
	probeName := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = s.local.RemoveTemporary(context.WithoutCancel(ctx), probeName)
		return operations.Operation{}, closeErr
	}
	if err := s.local.RemoveTemporary(context.WithoutCancel(ctx), probeName); err != nil {
		return operations.Operation{}, err
	}
	snapshot.SourceType = "remote"
	snapshot.SourceConnectionID = connectionID
	created, err := s.operations.Create("file.download", snapshot)
	if err != nil {
		return operations.Operation{}, err
	}
	return s.operations.Start(created.ID, &downloadRunner{remote: s.remote, local: s.local,
		connectionID: connectionID, sources: unique, destination: destination})
}

type downloadRunner struct {
	remote       remoteDownloadSource
	local        downloadDestination
	connectionID string
	sources      []string
	destination  string
	progress     operations.Progress
	copied       uint64
}

func (r *downloadRunner) Run(ctx context.Context, reporter operations.Reporter) error {
	entries := make([]core.FileEntry, 0, len(r.sources))
	var totalBytes uint64
	for _, source := range r.sources {
		entry, err := r.remote.Lstat(ctx, r.connectionID, source)
		if err != nil {
			return r.failure(fmt.Errorf("SFTP-файл %q недоступен: %w", source, err))
		}
		if entry.Kind != core.KindFile {
			return r.failure(fmt.Errorf("SFTP-объект %q больше не является обычным файлом", source))
		}
		entries = append(entries, entry)
		if entry.Size > 0 {
			totalBytes += uint64(entry.Size)
		}
	}
	totalFiles := uint64(len(entries))
	r.progress.TotalFiles, r.progress.TotalBytes = &totalFiles, &totalBytes
	if err := reporter.Report(r.progress); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := r.downloadFile(ctx, reporter, entry, filepath.Join(r.destination, path.Base(entry.Path))); err != nil {
			return r.failure(err)
		}
	}
	r.progress.CurrentFile = ""
	return reporter.Report(r.progress)
}

func (r *downloadRunner) failure(err error) error {
	if err == nil {
		return nil
	}
	code := "download_failed"
	message := fmt.Sprintf("Скачивание завершилось ошибкой: %v", err)
	if errors.Is(err, context.Canceled) {
		code, message = "download_cancelled", fmt.Sprintf("Скачивание отменено после %d файлов.", r.copied)
	} else if r.copied > 0 {
		code, message = "download_partial", fmt.Sprintf("Скачивание выполнено частично (%d файлов), затем произошла ошибка: %v", r.copied, err)
	}
	return &operations.RunError{Code: code, Message: message, Cause: err}
}

func (r *downloadRunner) downloadFile(ctx context.Context, reporter operations.Reporter, source core.FileEntry, destination string) (err error) {
	keepBothBase := destination
	action, fingerprint, err := r.destinationAction(ctx, reporter, source, destination)
	if err != nil || action == operations.Skip {
		return err
	}
	if action == operations.KeepBoth {
		destination, err = operations.NextKeepBothPath(keepBothBase, func(candidate string) (bool, error) {
			_, statErr := r.local.Lstat(ctx, candidate)
			return statErr == nil, ignoreNotExist(statErr)
		})
		if err != nil {
			return err
		}
		action = "new"
	}
	temporary, err := r.streamToTemporary(ctx, reporter, source, filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer func() {
		if temporary != "" {
			err = errors.Join(err, r.local.RemoveTemporary(context.WithoutCancel(ctx), temporary))
		}
	}()
	for {
		switch action {
		case "new":
			err = r.local.PublishNew(ctx, temporary, destination)
			if errors.Is(err, fs.ErrExist) {
				action, fingerprint, err = r.destinationAction(ctx, reporter, source, destination)
				if err != nil || action == operations.Skip {
					return err
				}
				if action == operations.KeepBoth {
					destination, err = operations.NextKeepBothPath(keepBothBase, func(candidate string) (bool, error) {
						_, statErr := r.local.Lstat(ctx, candidate)
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
			err = r.local.PublishReplace(ctx, temporary, destination, fingerprint)
		default:
			return fmt.Errorf("неподдерживаемое решение конфликта: %s", action)
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

func (r *downloadRunner) streamToTemporary(ctx context.Context, reporter operations.Reporter, source core.FileEntry, directory string) (result string, err error) {
	input, err := r.remote.Open(ctx, r.connectionID, source.Path)
	if err != nil {
		return "", fmt.Errorf("открыть SFTP-файл %q: %w", source.Path, err)
	}
	defer func() { _ = input.Close() }()
	output, err := r.local.CreateTemporaryFile(ctx, directory, source.Mode)
	if err != nil {
		return "", fmt.Errorf("создать временный файл: %w", err)
	}
	temporary := output.Name()
	ok := false
	defer func() {
		if !ok {
			_ = output.Close()
			_ = r.local.RemoveTemporary(context.WithoutCancel(ctx), temporary)
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
			return "", fmt.Errorf("прочитать SFTP-файл %q: %w", source.Path, readErr)
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

func (r *downloadRunner) destinationAction(ctx context.Context, reporter operations.Reporter, source core.FileEntry,
	destination string) (operations.ConflictAction, core.FileFingerprint, error) {
	destinationInfo, err := r.local.Lstat(ctx, destination)
	if errors.Is(err, fs.ErrNotExist) {
		return "new", core.FileFingerprint{}, nil
	}
	if err != nil {
		return "", core.FileFingerprint{}, fmt.Errorf("проверить назначение %q: %w", destination, err)
	}
	typeOf := operations.FileFile
	if destinationInfo.Kind == core.KindDirectory {
		typeOf = operations.FileDirectory
	}
	if destinationInfo.Kind == core.KindOther {
		return "", core.FileFingerprint{}, fmt.Errorf("назначение является специальным объектом")
	}
	fingerprint, err := r.local.Fingerprint(ctx, destination)
	if err != nil {
		return "", core.FileFingerprint{}, err
	}
	decision, err := reporter.RequestConflict(operations.ConflictRequest{Type: typeOf,
		Source: conflictMetadata(source), Destination: conflictMetadata(destinationInfo)})
	if err != nil {
		return "", core.FileFingerprint{}, err
	}
	return decision.Resolution.Action, fingerprint, nil
}
