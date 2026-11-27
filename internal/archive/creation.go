package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

// ArchiveCreateFileSystem is the local copy contract shared by archive
// creators. It deliberately uses the same safe publication primitives as
// ordinary copy operations.
type ArchiveCreateFileSystem interface {
	core.FileSystem
	core.CopySource
	core.CopyDestination
}

type ArchiveCreationReport struct {
	ArchivedFiles uint64
	ArchivedBytes uint64
	Skipped       bool
}

type archiveCreationEntry struct {
	source   string
	name     string
	kind     core.EntryKind
	size     int64
	mode     fs.FileMode
	modified time.Time
}

type archiveCreationResolver func(context.Context, core.FileEntry) (operations.ConflictAction, error)

type archiveContentWriter func(context.Context, ArchiveCreateFileSystem, io.Writer, []archiveCreationEntry,
	*uint64, ExtractionProgress) (ArchiveCreationReport, error)

func createArchive(ctx context.Context, filesystem ArchiveCreateFileSystem, sources []string, destination,
	formatName string, resolver archiveCreationResolver, progress ExtractionProgress,
	write archiveContentWriter) (report ArchiveCreationReport, resultErr error) {
	if ctx == nil {
		panic("archive: nil context")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if filesystem == nil || !filepath.IsAbs(destination) || write == nil {
		return report, fmt.Errorf("%w: filesystem, writer and absolute destination are required", ErrInvalidMetadata)
	}
	destination = filepath.Clean(destination)
	plan, totalBytes, err := buildArchiveCreationPlan(ctx, filesystem, sources, destination, formatName)
	if err != nil {
		return report, err
	}
	action, expected, skipped, err := resolveArchiveCreationConflict(ctx, filesystem, destination, formatName, resolver)
	if err != nil {
		return report, err
	}
	if skipped {
		report.Skipped = true
		return report, nil
	}
	temporary, err := filesystem.CreateTemporaryFile(ctx, filepath.Dir(destination), 0o600)
	if err != nil {
		return report, err
	}
	temporaryName := temporary.Name()
	published := false
	temporaryClosed := false
	defer func() {
		if published || errors.Is(resultErr, core.ErrTemporaryHoldsDestination) {
			return
		}
		cleanupCtx := context.WithoutCancel(ctx)
		var closeErr error
		if !temporaryClosed {
			closeErr = temporary.Close()
		}
		resultErr = errors.Join(resultErr, closeErr, filesystem.RemoveTemporary(cleanupCtx, temporaryName))
	}()

	report, err = write(ctx, filesystem, temporary, plan, totalBytes, progress)
	if err != nil {
		return report, err
	}
	if err := temporary.Sync(); err != nil {
		return report, err
	}
	err = temporary.Close()
	temporaryClosed = true
	if err != nil {
		return report, err
	}

	for {
		switch action {
		case "new":
			err = filesystem.PublishNew(ctx, temporaryName, destination)
			if err != nil && (errors.Is(err, fs.ErrExist) || os.IsExist(err)) {
				action, expected, skipped, err = resolveArchiveCreationConflict(ctx, filesystem, destination, formatName, resolver)
				if err == nil && skipped {
					report.Skipped = true
					return report, nil
				}
				if err == nil {
					continue
				}
			}
		case operations.Replace:
			err = filesystem.PublishReplace(ctx, temporaryName, destination, expected)
		default:
			err = fmt.Errorf("%w: unsupported %s publication action %q", ErrConflict, formatName, action)
		}
		if err != nil {
			return report, err
		}
		published = true
		return report, nil
	}
}

func buildArchiveCreationPlan(ctx context.Context, filesystem ArchiveCreateFileSystem, sources []string,
	destination, formatName string) ([]archiveCreationEntry, *uint64, error) {
	plan := make([]archiveCreationEntry, 0)
	names := make(map[string]struct{})
	var total uint64
	totalKnown := true
	var walk func(core.FileEntry, string) error
	walk = func(entry core.FileEntry, name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if filepath.Clean(entry.Path) == destination {
			return nil
		}
		archiveName := filepath.ToSlash(name)
		if _, err := ValidateExtractionPath(strings.TrimSuffix(archiveName, "/")); err != nil {
			return err
		}
		if _, duplicate := names[archiveName]; duplicate {
			return pathError(archiveName, fmt.Errorf("%w: duplicate %s path", ErrConflict, formatName))
		}
		names[archiveName] = struct{}{}
		switch entry.Kind {
		case core.KindFile:
			if entry.Size < 0 {
				return pathError(archiveName, ErrInvalidMetadata)
			}
			plan = append(plan, archiveCreationEntry{source: entry.Path, name: archiveName, kind: entry.Kind,
				size: entry.Size, mode: entry.Mode, modified: entry.ModifiedAt})
			if uint64(entry.Size) > ^uint64(0)-total {
				totalKnown = false
			} else {
				total += uint64(entry.Size)
			}
		case core.KindDirectory:
			plan = append(plan, archiveCreationEntry{source: entry.Path, name: archiveName, kind: entry.Kind,
				mode: entry.Mode, modified: entry.ModifiedAt})
			children, err := filesystem.ReadCopyDirectory(ctx, entry.Path)
			if err != nil {
				return err
			}
			sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
			for _, child := range children {
				if filepath.Clean(child.Path) == destination {
					continue
				}
				if err := walk(child, filepath.Join(name, child.Name)); err != nil {
					return err
				}
			}
		case core.KindSymlink, core.KindOther:
			return pathError(archiveName, fmt.Errorf("%w: local %s", ErrUnsupportedEntry, entry.Kind))
		default:
			return pathError(archiveName, ErrUnsupportedEntry)
		}
		return nil
	}
	for _, source := range sources {
		if !filepath.IsAbs(source) {
			return nil, nil, fmt.Errorf("%w: source path must be absolute", ErrInvalidMetadata)
		}
		source = filepath.Clean(source)
		if source == destination {
			return nil, nil, fmt.Errorf("%w: destination %s cannot archive itself", ErrConflict, formatName)
		}
		entry, err := filesystem.Lstat(ctx, source)
		if err != nil {
			return nil, nil, err
		}
		if err := walk(entry, filepath.Base(source)); err != nil {
			return nil, nil, err
		}
	}
	if !totalKnown {
		return plan, nil, nil
	}
	return plan, &total, nil
}

func resolveArchiveCreationConflict(ctx context.Context, filesystem ArchiveCreateFileSystem, destination,
	formatName string, resolver archiveCreationResolver) (operations.ConflictAction, core.FileFingerprint, bool, error) {
	entry, err := filesystem.Lstat(ctx, destination)
	if errors.Is(err, fs.ErrNotExist) {
		return "new", core.FileFingerprint{}, false, nil
	}
	if err != nil {
		return "", core.FileFingerprint{}, false, err
	}
	if entry.Kind != core.KindFile {
		return "", core.FileFingerprint{}, false, fmt.Errorf("%w: %s destination is %s", ErrConflict, formatName, entry.Kind)
	}
	if resolver == nil {
		return "", core.FileFingerprint{}, false, fmt.Errorf("%w: %s destination already exists", ErrConflict, formatName)
	}
	action, err := resolver(ctx, entry)
	if err != nil {
		return "", core.FileFingerprint{}, false, err
	}
	switch action {
	case operations.Skip:
		return action, core.FileFingerprint{}, true, nil
	case operations.Replace:
		fingerprint, err := filesystem.Fingerprint(ctx, destination)
		return action, fingerprint, false, err
	default:
		return "", core.FileFingerprint{}, false, fmt.Errorf("%w: unsupported %s conflict action %q", ErrConflict, formatName, action)
	}
}

func copyArchiveEntry(ctx context.Context, destination io.Writer, source io.Reader, name string, baseBytes,
	files, totalFiles uint64, totalBytes *uint64, progress ExtractionProgress) (uint64, error) {
	buffer := make([]byte, extractionBufferSize)
	var written uint64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			amount, writeErr := destination.Write(buffer[:count])
			written += uint64(amount)
			if writeErr != nil {
				return written, writeErr
			}
			if amount != count {
				return written, io.ErrShortWrite
			}
			if err := reportArchiveCreationProgress(progress, name, files, totalFiles, baseBytes+written, totalBytes); err != nil {
				return written, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
		if count == 0 {
			return written, io.ErrNoProgress
		}
	}
}

func reportArchiveCreationProgress(report ExtractionProgress, name string, files, totalFiles, bytes uint64,
	totalBytes *uint64) error {
	if report == nil {
		return nil
	}
	return report(operations.Progress{ProcessedFiles: files, TotalFiles: &totalFiles, ProcessedBytes: bytes,
		TotalBytes: totalBytes, CurrentFile: name})
}

func countArchiveFiles(plan []archiveCreationEntry) uint64 {
	var result uint64
	for _, item := range plan {
		if item.kind == core.KindFile {
			result++
		}
	}
	return result
}
