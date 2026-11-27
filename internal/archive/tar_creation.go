package archive

import (
	archivetar "archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type TARCreateFileSystem = ArchiveCreateFileSystem
type TARCreationReport = ArchiveCreationReport

type TARCreationConflict struct {
	Destination core.FileEntry
}

type TARCreationConflictResolver interface {
	ResolveTARCreationConflict(context.Context, TARCreationConflict) (operations.ConflictAction, error)
}

type TARCreationConflictResolverFunc func(context.Context, TARCreationConflict) (operations.ConflictAction, error)

func (f TARCreationConflictResolverFunc) ResolveTARCreationConflict(ctx context.Context,
	conflict TARCreationConflict) (operations.ConflictAction, error) {
	return f(ctx, conflict)
}

type TARCreationOptions struct {
	ConflictResolver TARCreationConflictResolver
	Progress         ExtractionProgress
	// GzipLevel is used by CreateTARGZ. Zero selects gzip.DefaultCompression;
	// other values follow compress/gzip.NewWriterLevel.
	GzipLevel int
}

func CreateTAR(ctx context.Context, filesystem TARCreateFileSystem, sources []string, destination string,
	options TARCreationOptions) (TARCreationReport, error) {
	return createArchive(ctx, filesystem, sources, destination, "TAR", tarCreationResolver(options),
		options.Progress, writeTARContent)
}

func CreateTARGZ(ctx context.Context, filesystem TARCreateFileSystem, sources []string, destination string,
	options TARCreationOptions) (TARCreationReport, error) {
	level := options.GzipLevel
	if level == 0 {
		level = gzip.DefaultCompression
	}
	write := func(ctx context.Context, filesystem ArchiveCreateFileSystem, destination io.Writer,
		plan []archiveCreationEntry, totalBytes *uint64, progress ExtractionProgress) (ArchiveCreationReport, error) {
		return writeTARGZContent(ctx, filesystem, destination, plan, totalBytes, progress, level)
	}
	return createArchive(ctx, filesystem, sources, destination, "TAR.GZ", tarCreationResolver(options),
		options.Progress, write)
}

func tarCreationResolver(options TARCreationOptions) archiveCreationResolver {
	if options.ConflictResolver == nil {
		return nil
	}
	return func(ctx context.Context, destination core.FileEntry) (operations.ConflictAction, error) {
		return options.ConflictResolver.ResolveTARCreationConflict(ctx, TARCreationConflict{Destination: destination})
	}
}

func writeTARContent(ctx context.Context, filesystem ArchiveCreateFileSystem, destination io.Writer,
	plan []archiveCreationEntry, totalBytes *uint64, progress ExtractionProgress) (ArchiveCreationReport, error) {
	return writeTARStream(ctx, filesystem, destination, plan, totalBytes, progress)
}

func writeTARGZContent(ctx context.Context, filesystem ArchiveCreateFileSystem, destination io.Writer,
	plan []archiveCreationEntry, totalBytes *uint64, progress ExtractionProgress,
	level int) (report ArchiveCreationReport, resultErr error) {
	gzipWriter, err := gzip.NewWriterLevel(destination, level)
	if err != nil {
		return report, err
	}
	closed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, gzipWriter.Close())
		}
	}()
	report, resultErr = writeTARStream(ctx, filesystem, gzipWriter, plan, totalBytes, progress)
	closeErr := gzipWriter.Close()
	closed = true
	return report, errors.Join(resultErr, closeErr)
}

func writeTARStream(ctx context.Context, filesystem ArchiveCreateFileSystem, destination io.Writer,
	plan []archiveCreationEntry, totalBytes *uint64, progress ExtractionProgress) (report ArchiveCreationReport, resultErr error) {
	writer := archivetar.NewWriter(destination)
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
		header := &archivetar.Header{
			Name:    item.name,
			Mode:    int64(item.mode.Perm()),
			ModTime: item.modified,
			Format:  archivetar.FormatPAX,
		}
		if item.kind == core.KindDirectory {
			header.Name = strings.TrimSuffix(header.Name, "/") + "/"
			header.Typeflag = archivetar.TypeDir
			if err := writer.WriteHeader(header); err != nil {
				return report, err
			}
			continue
		}
		header.Typeflag = archivetar.TypeReg
		header.Size = item.size
		if err := writer.WriteHeader(header); err != nil {
			return report, err
		}
		input, err := filesystem.OpenCopyFile(ctx, item.source)
		if err != nil {
			return report, err
		}
		written, copyErr := copyArchiveEntry(ctx, writer, input, item.name, report.ArchivedBytes,
			report.ArchivedFiles, totalFiles, totalBytes, progress)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			return report, errors.Join(copyErr, closeErr)
		}
		if written != uint64(item.size) {
			return report, io.ErrUnexpectedEOF
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
