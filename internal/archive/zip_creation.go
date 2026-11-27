package archive

import (
	archivezip "archive/zip"
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

// ZIPCreateFileSystem is the existing local copy contract required to read
// sources without following final links and publish the completed archive
// without an unapproved overwrite.
type ZIPCreateFileSystem interface {
	core.FileSystem
	core.CopySource
	core.CopyDestination
}

type ZIPCreationConflict struct {
	Destination core.FileEntry
}

type ZIPCreationConflictResolver interface {
	ResolveZIPCreationConflict(context.Context, ZIPCreationConflict) (operations.ConflictAction, error)
}

type ZIPCreationConflictResolverFunc func(context.Context, ZIPCreationConflict) (operations.ConflictAction, error)

func (f ZIPCreationConflictResolverFunc) ResolveZIPCreationConflict(ctx context.Context, conflict ZIPCreationConflict) (operations.ConflictAction, error) {
	return f(ctx, conflict)
}

type ZIPCreationOptions struct {
	ConflictResolver ZIPCreationConflictResolver
	Progress         ExtractionProgress
}

type ZIPCreationReport struct {
	ArchivedFiles uint64
	ArchivedBytes uint64
	Skipped       bool
}

type zipCreationEntry struct {
	source   string
	name     string
	kind     core.EntryKind
	size     int64
	mode     fs.FileMode
	modified time.Time
}

// CreateZIP writes a new ZIP from captured local paths. The ZIP is finalized
// and synced in a private temporary file before CopyDestination publishes it.
func CreateZIP(ctx context.Context, filesystem ZIPCreateFileSystem, sources []string, destination string,
	options ZIPCreationOptions) (report ZIPCreationReport, resultErr error) {
	if ctx == nil {
		panic("archive: nil context")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if filesystem == nil || !filepath.IsAbs(destination) {
		return report, fmt.Errorf("%w: filesystem and absolute destination are required", ErrInvalidMetadata)
	}
	destination = filepath.Clean(destination)
	plan, totalBytes, err := buildZIPCreationPlan(ctx, filesystem, sources, destination)
	if err != nil {
		return report, err
	}
	action, expected, skipped, err := resolveZIPCreationConflict(ctx, filesystem, destination, options.ConflictResolver)
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

	writer := archivezip.NewWriter(temporary)
	totalFiles := countZIPFiles(plan)
	for _, item := range plan {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return report, err
		}
		header := &archivezip.FileHeader{Name: item.name}
		header.SetMode(item.mode)
		if !item.modified.IsZero() {
			header.SetModTime(item.modified)
		}
		if item.kind == core.KindDirectory {
			header.Name = strings.TrimSuffix(header.Name, "/") + "/"
			header.Method = archivezip.Store
			if _, err := writer.CreateHeader(header); err != nil {
				_ = writer.Close()
				return report, err
			}
			continue
		}
		header.Method = archivezip.Deflate
		input, err := filesystem.OpenCopyFile(ctx, item.source)
		if err != nil {
			_ = writer.Close()
			return report, err
		}
		output, err := writer.CreateHeader(header)
		if err != nil {
			_ = input.Close()
			_ = writer.Close()
			return report, err
		}
		written, copyErr := copyZIPEntry(ctx, output, input, item.name, report.ArchivedBytes,
			report.ArchivedFiles, totalFiles, totalBytes, options.Progress)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			_ = writer.Close()
			return report, errors.Join(copyErr, closeErr)
		}
		report.ArchivedBytes += written
		report.ArchivedFiles++
		if err := reportZIPCreationProgress(options.Progress, item.name, report.ArchivedFiles, totalFiles,
			report.ArchivedBytes, totalBytes); err != nil {
			_ = writer.Close()
			return report, err
		}
	}
	if err := writer.Close(); err != nil {
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
				action, expected, skipped, err = resolveZIPCreationConflict(ctx, filesystem, destination, options.ConflictResolver)
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
			err = fmt.Errorf("%w: unsupported ZIP publication action %q", ErrConflict, action)
		}
		if err != nil {
			return report, err
		}
		published = true
		return report, nil
	}
}

func buildZIPCreationPlan(ctx context.Context, filesystem ZIPCreateFileSystem, sources []string, destination string) ([]zipCreationEntry, *uint64, error) {
	plan := make([]zipCreationEntry, 0)
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
			return pathError(archiveName, fmt.Errorf("%w: duplicate ZIP path", ErrConflict))
		}
		names[archiveName] = struct{}{}
		switch entry.Kind {
		case core.KindFile:
			if entry.Size < 0 {
				return pathError(archiveName, ErrInvalidMetadata)
			}
			plan = append(plan, zipCreationEntry{source: entry.Path, name: archiveName, kind: entry.Kind,
				size: entry.Size, mode: entry.Mode, modified: entry.ModifiedAt})
			if uint64(entry.Size) > ^uint64(0)-total {
				totalKnown = false
			} else {
				total += uint64(entry.Size)
			}
		case core.KindDirectory:
			plan = append(plan, zipCreationEntry{source: entry.Path, name: archiveName, kind: entry.Kind,
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
			return nil, nil, fmt.Errorf("%w: destination ZIP cannot archive itself", ErrConflict)
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

func resolveZIPCreationConflict(ctx context.Context, filesystem ZIPCreateFileSystem, destination string,
	resolver ZIPCreationConflictResolver) (operations.ConflictAction, core.FileFingerprint, bool, error) {
	entry, err := filesystem.Lstat(ctx, destination)
	if errors.Is(err, fs.ErrNotExist) {
		return "new", core.FileFingerprint{}, false, nil
	}
	if err != nil {
		return "", core.FileFingerprint{}, false, err
	}
	if entry.Kind != core.KindFile {
		return "", core.FileFingerprint{}, false, fmt.Errorf("%w: ZIP destination is %s", ErrConflict, entry.Kind)
	}
	if resolver == nil {
		return "", core.FileFingerprint{}, false, fmt.Errorf("%w: ZIP destination already exists", ErrConflict)
	}
	action, err := resolver.ResolveZIPCreationConflict(ctx, ZIPCreationConflict{Destination: entry})
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
		return "", core.FileFingerprint{}, false, fmt.Errorf("%w: unsupported ZIP conflict action %q", ErrConflict, action)
	}
}

func copyZIPEntry(ctx context.Context, destination io.Writer, source io.Reader, name string, baseBytes, files,
	totalFiles uint64, totalBytes *uint64, progress ExtractionProgress) (uint64, error) {
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
			if err := reportZIPCreationProgress(progress, name, files, totalFiles, baseBytes+written, totalBytes); err != nil {
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

func reportZIPCreationProgress(report ExtractionProgress, name string, files, totalFiles, bytes uint64, totalBytes *uint64) error {
	if report == nil {
		return nil
	}
	return report(operations.Progress{ProcessedFiles: files, TotalFiles: &totalFiles, ProcessedBytes: bytes,
		TotalBytes: totalBytes, CurrentFile: name})
}

func countZIPFiles(plan []zipCreationEntry) uint64 {
	var result uint64
	for _, item := range plan {
		if item.kind == core.KindFile {
			result++
		}
	}
	return result
}
