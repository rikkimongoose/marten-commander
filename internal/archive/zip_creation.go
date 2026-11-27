package archive

import (
	archivezip "archive/zip"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type ZIPCreateFileSystem = ArchiveCreateFileSystem

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

type ZIPCreationReport = ArchiveCreationReport

// CreateZIP writes a new ZIP from captured local paths. The ZIP is finalized
// and synced in a private temporary file before CopyDestination publishes it.
func CreateZIP(ctx context.Context, filesystem ZIPCreateFileSystem, sources []string, destination string,
	options ZIPCreationOptions) (ZIPCreationReport, error) {
	var resolver archiveCreationResolver
	if options.ConflictResolver != nil {
		resolver = func(ctx context.Context, destination core.FileEntry) (operations.ConflictAction, error) {
			return options.ConflictResolver.ResolveZIPCreationConflict(ctx, ZIPCreationConflict{Destination: destination})
		}
	}
	return createArchive(ctx, filesystem, sources, destination, "ZIP", resolver, options.Progress, writeZIPContent)
}

func writeZIPContent(ctx context.Context, filesystem ArchiveCreateFileSystem, destination io.Writer,
	plan []archiveCreationEntry, totalBytes *uint64, progress ExtractionProgress) (report ArchiveCreationReport, resultErr error) {
	writer := archivezip.NewWriter(destination)
	closed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, writer.Close())
		}
	}()
	totalFiles := countArchiveFiles(plan)
	for _, item := range plan {
		if err := ctx.Err(); err != nil {
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
				return report, err
			}
			continue
		}
		header.Method = archivezip.Deflate
		input, err := filesystem.OpenCopyFile(ctx, item.source)
		if err != nil {
			return report, err
		}
		output, err := writer.CreateHeader(header)
		if err != nil {
			return report, errors.Join(err, input.Close())
		}
		written, copyErr := copyArchiveEntry(ctx, output, input, item.name, report.ArchivedBytes,
			report.ArchivedFiles, totalFiles, totalBytes, progress)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			return report, errors.Join(copyErr, closeErr)
		}
		report.ArchivedBytes += written
		report.ArchivedFiles++
		if err := reportArchiveCreationProgress(progress, item.name, report.ArchivedFiles, totalFiles,
			report.ArchivedBytes, totalBytes); err != nil {
			return report, err
		}
	}
	err := writer.Close()
	closed = true
	return report, err
}
