package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/rikkimongoose/marten-commander/internal/operations"
)

const extractionBufferSize = 128 * 1024

// ExtractionLimits bounds both declared metadata and bytes actually streamed.
// Every field must be positive. Use DefaultExtractionLimits as a safe baseline.
type ExtractionLimits struct {
	MaxEntries        uint64
	MaxFileSize       uint64
	MaxTotalSize      uint64
	MaxDepth          uint64
	MaxExpansionRatio uint64
}

func DefaultExtractionLimits() ExtractionLimits {
	return ExtractionLimits{
		MaxEntries: 100_000, MaxFileSize: 4 << 30, MaxTotalSize: 20 << 30,
		MaxDepth: 64, MaxExpansionRatio: 1_000,
	}
}

type ExtractionStage string

const (
	StagePlan     ExtractionStage = "plan"
	StageConflict ExtractionStage = "conflict"
	StageOpen     ExtractionStage = "open"
	StageWrite    ExtractionStage = "write"
	StagePublish  ExtractionStage = "publish"
)

// ExtractionError retains the entry and stage at which extraction stopped.
type ExtractionError struct {
	Path  string
	Stage ExtractionStage
	Err   error
}

func (e *ExtractionError) Error() string {
	return fmt.Sprintf("extract %q at %s: %v", e.Path, e.Stage, e.Err)
}
func (e *ExtractionError) Unwrap() error { return e.Err }

type SkippedEntry struct {
	Path   string
	Reason string
}

// ExtractionReport remains useful when Extract returns an error: Extracted and
// Skipped contain only operations completed before FailedPath/FailedStage.
type ExtractionReport struct {
	Extracted    []string
	Skipped      []SkippedEntry
	BytesWritten uint64
	FailedPath   string
	FailedStage  ExtractionStage
}

type DestinationKind string

const (
	DestinationFile      DestinationKind = "file"
	DestinationDirectory DestinationKind = "directory"
	DestinationSymlink   DestinationKind = "symlink"
	DestinationOther     DestinationKind = "other"
)

type DestinationEntry struct {
	Kind     DestinationKind
	Token    string
	Identity string
}

// ExtractionDestination contains only descriptor-safe operations required by
// Extractor. Paths are validated relative POSIX paths, never host OS paths.
type ExtractionDestination interface {
	EnsureDirectory(context.Context, string) error
	Inspect(context.Context, string) (DestinationEntry, error)
	CreateTemporary(context.Context, string) (ExtractionFile, error)
}

// ExtractionFile is a private temporary file in the destination directory.
// Publish methods consume it on success; Discard is idempotent.
type ExtractionFile interface {
	io.WriteCloser
	Sync() error
	PublishNew(context.Context) error
	PublishReplace(context.Context, string, string) error
	Discard(context.Context) error
}

type ExtractionConflict struct {
	Source      ArchiveEntry
	Destination DestinationEntry
}

// ExtractionConflictResolver adapts extraction conflicts to the existing
// operation conflict vocabulary. Extractor accepts only Skip or Replace.
type ExtractionConflictResolver interface {
	ResolveExtractionConflict(context.Context, ExtractionConflict) (operations.ConflictAction, error)
}

type ExtractionConflictResolverFunc func(context.Context, ExtractionConflict) (operations.ConflictAction, error)

func (f ExtractionConflictResolverFunc) ResolveExtractionConflict(ctx context.Context, conflict ExtractionConflict) (operations.ConflictAction, error) {
	return f(ctx, conflict)
}

type Extractor struct{ limits ExtractionLimits }

func NewExtractor(limits ExtractionLimits) (*Extractor, error) {
	if limits.MaxEntries == 0 || limits.MaxFileSize == 0 || limits.MaxTotalSize == 0 ||
		limits.MaxDepth == 0 || limits.MaxExpansionRatio == 0 {
		return nil, fmt.Errorf("%w: extraction limits must be positive", ErrInvalidMetadata)
	}
	return &Extractor{limits: limits}, nil
}

func NewDefaultExtractor() *Extractor {
	extractor, _ := NewExtractor(DefaultExtractionLimits())
	return extractor
}

// Extract validates the complete archive tree before writing, then extracts it
// in directory-first order. It never follows archive links or destination
// links and never publishes a partial file.
func (e *Extractor) Extract(ctx context.Context, session ArchiveSession, destination ExtractionDestination,
	resolver ExtractionConflictResolver) (ExtractionReport, error) {
	var report ExtractionReport
	if ctx == nil {
		panic("archive: nil context")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if e == nil || session == nil || destination == nil {
		return report, fmt.Errorf("%w: extractor, session and destination are required", ErrExtractionFailed)
	}
	plan, err := e.buildPlan(ctx, session)
	if err != nil {
		var extractionErr *ExtractionError
		if errors.As(err, &extractionErr) {
			return failReport(report, extractionErr.Path, extractionErr.Stage, extractionErr.Err)
		}
		return failReport(report, "", StagePlan, err)
	}
	for _, item := range plan {
		if err := ctx.Err(); err != nil {
			return failReport(report, item.relative, StageOpen, err)
		}
		if item.entry.Kind == EntryDirectory {
			if err := destination.EnsureDirectory(ctx, item.relative); err != nil {
				return failReport(report, item.relative, StagePublish, err)
			}
			report.Extracted = append(report.Extracted, item.relative)
			continue
		}
		if item.entry.Kind != EntryFile {
			return failReport(report, item.relative, StagePlan,
				pathError(item.entry.RawPath, fmt.Errorf("%w: %s", ErrUnsupportedEntry, item.entry.Kind)))
		}
		action, expected, skipped, err := chooseConflict(ctx, destination, resolver, item.entry, item.relative)
		if err != nil {
			return failReport(report, item.relative, StageConflict, err)
		}
		if skipped {
			report.Skipped = append(report.Skipped, SkippedEntry{Path: item.relative, Reason: "conflict"})
			continue
		}
		written, skipped, err := e.extractFile(ctx, session, destination, resolver, item, action, expected, &report)
		if err != nil {
			var extractionErr *ExtractionError
			if errors.As(err, &extractionErr) {
				return failReport(report, item.relative, extractionErr.Stage, err)
			}
			return failReport(report, item.relative, StageWrite, err)
		}
		if skipped {
			report.Skipped = append(report.Skipped, SkippedEntry{Path: item.relative, Reason: "conflict"})
			continue
		}
		report.BytesWritten, err = addChecked(report.BytesWritten, written, e.limits.MaxTotalSize)
		if err != nil {
			return failReport(report, item.relative, StageWrite, err)
		}
		report.Extracted = append(report.Extracted, item.relative)
	}
	return report, nil
}

type plannedEntry struct {
	entry    ArchiveEntry
	relative string
}

func (e *Extractor) buildPlan(ctx context.Context, session ArchiveSession) ([]plannedEntry, error) {
	plan := make([]plannedEntry, 0)
	var declaredTotal uint64
	var walk func(string) error
	walk = func(directory string) error {
		entries, err := session.ReadDir(ctx, directory)
		if err != nil {
			return &ExtractionError{Path: directory, Stage: StagePlan, Err: err}
		}
		for _, entry := range entries {
			raw := entry.RawPath
			if raw == "" {
				return &ExtractionError{Path: entry.Path, Stage: StagePlan,
					Err: pathError(entry.Path, fmt.Errorf("%w: missing raw archive path", ErrInvalidMetadata))}
			}
			validationPath := raw
			if entry.Kind == EntryDirectory && strings.HasSuffix(validationPath, "/") {
				validationPath = strings.TrimSuffix(validationPath, "/")
			}
			if err := ctx.Err(); err != nil {
				return &ExtractionError{Path: raw, Stage: StagePlan, Err: err}
			}
			if uint64(len(plan)) >= e.limits.MaxEntries {
				return &ExtractionError{Path: raw, Stage: StagePlan,
					Err: fmt.Errorf("%w: maximum entry count %d", ErrLimitExceeded, e.limits.MaxEntries)}
			}
			relative, err := ValidateExtractionPath(validationPath)
			if err != nil {
				return &ExtractionError{Path: raw, Stage: StagePlan, Err: err}
			}
			depth := uint64(strings.Count(relative, "/") + 1)
			if depth > e.limits.MaxDepth {
				return &ExtractionError{Path: relative, Stage: StagePlan,
					Err: fmt.Errorf("%w: path exceeds depth %d", ErrLimitExceeded, e.limits.MaxDepth)}
			}
			if entry.Size != nil && *entry.Size < 0 || entry.CompressedSize != nil && *entry.CompressedSize < 0 {
				return &ExtractionError{Path: relative, Stage: StagePlan, Err: pathError(raw, ErrInvalidMetadata)}
			}
			if entry.Kind != EntryFile && entry.Kind != EntryDirectory {
				return &ExtractionError{Path: relative, Stage: StagePlan,
					Err: pathError(raw, fmt.Errorf("%w: %s", ErrUnsupportedEntry, entry.Kind))}
			}
			if entry.Kind == EntryFile && entry.Size != nil {
				size := uint64(*entry.Size)
				if size > e.limits.MaxFileSize {
					return &ExtractionError{Path: relative, Stage: StagePlan,
						Err: fmt.Errorf("%w: file exceeds %d bytes", ErrLimitExceeded, e.limits.MaxFileSize)}
				}
				declaredTotal, err = addChecked(declaredTotal, size, e.limits.MaxTotalSize)
				if err != nil {
					return &ExtractionError{Path: relative, Stage: StagePlan, Err: err}
				}
				if err := checkRatio(size, entry.CompressedSize, e.limits.MaxExpansionRatio); err != nil {
					return &ExtractionError{Path: relative, Stage: StagePlan, Err: pathError(raw, err)}
				}
			}
			plan = append(plan, plannedEntry{entry: entry, relative: relative})
			if entry.Kind == EntryDirectory {
				if err := walk(entry.Path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk("/"); err != nil {
		return nil, err
	}
	return plan, nil
}

func ValidateExtractionPath(name string) (string, error) {
	if name == "" || !utf8.ValidString(name) || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
		return "", pathError(name, ErrUnsafePath)
	}
	parts := strings.Split(name, "/")
	for index, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") ||
			index == 0 && len(part) == 2 && ((part[0] >= 'a' && part[0] <= 'z') || (part[0] >= 'A' && part[0] <= 'Z')) && part[1] == ':' {
			return "", pathError(name, ErrUnsafePath)
		}
	}
	return strings.Join(parts, "/"), nil
}

func chooseConflict(ctx context.Context, destination ExtractionDestination, resolver ExtractionConflictResolver,
	entry ArchiveEntry, relative string) (operations.ConflictAction, DestinationEntry, bool, error) {
	existing, err := destination.Inspect(ctx, relative)
	if errors.Is(err, ErrEntryNotFound) {
		return "new", DestinationEntry{}, false, nil
	}
	if err != nil {
		return "", DestinationEntry{}, false, err
	}
	if existing.Kind == DestinationSymlink {
		return "", DestinationEntry{}, false, fmt.Errorf("%w: destination %q is a symbolic link", ErrUnsafeDestination, relative)
	}
	if existing.Kind != DestinationFile {
		return "", DestinationEntry{}, false, fmt.Errorf("%w: destination %q is %s", ErrConflict, relative, existing.Kind)
	}
	if resolver == nil {
		return "", DestinationEntry{}, false, fmt.Errorf("%w: %q already exists", ErrConflict, relative)
	}
	action, err := resolver.ResolveExtractionConflict(ctx, ExtractionConflict{Source: entry, Destination: existing})
	if err != nil {
		return "", DestinationEntry{}, false, err
	}
	switch action {
	case operations.Skip:
		return action, existing, true, nil
	case operations.Replace:
		return action, existing, false, nil
	default:
		return "", DestinationEntry{}, false, fmt.Errorf("%w: unsupported resolution %q", ErrConflict, action)
	}
}

func (e *Extractor) extractFile(ctx context.Context, session ArchiveSession, destination ExtractionDestination,
	resolver ExtractionConflictResolver, item plannedEntry, action operations.ConflictAction, expected DestinationEntry,
	report *ExtractionReport) (written uint64, skipped bool, resultErr error) {
	input, err := session.Open(ctx, item.entry.Path)
	if err != nil {
		return 0, false, &ExtractionError{Path: item.relative, Stage: StageOpen, Err: err}
	}
	inputClosed := false
	defer func() {
		if !inputClosed {
			resultErr = errors.Join(resultErr, input.Close())
		}
	}()
	temporary, err := destination.CreateTemporary(ctx, item.relative)
	if err != nil {
		closeErr := input.Close()
		inputClosed = true
		return 0, false, &ExtractionError{Path: item.relative, Stage: StageOpen, Err: errors.Join(err, closeErr)}
	}
	published := false
	defer func() {
		if !published {
			cleanupCtx := context.WithoutCancel(ctx)
			resultErr = errors.Join(resultErr, temporary.Close(), temporary.Discard(cleanupCtx))
		}
	}()

	buffer := make([]byte, extractionBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: err}
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			nextFile, limitErr := addChecked(written, uint64(count), e.limits.MaxFileSize)
			if limitErr == nil {
				_, limitErr = addChecked(report.BytesWritten, nextFile, e.limits.MaxTotalSize)
			}
			if limitErr == nil {
				limitErr = checkRatio(nextFile, item.entry.CompressedSize, e.limits.MaxExpansionRatio)
			}
			if limitErr != nil {
				return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: limitErr}
			}
			amount, writeErr := temporary.Write(buffer[:count])
			written = nextFile
			if writeErr != nil {
				return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: writeErr}
			}
			if amount != count {
				return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: io.ErrShortWrite}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: readError("read "+item.entry.Path, readErr)}
		}
		if count == 0 {
			return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: io.ErrNoProgress}
		}
	}
	if err := input.Close(); err != nil {
		inputClosed = true
		return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: err}
	}
	inputClosed = true
	if err := temporary.Sync(); err != nil {
		return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: err}
	}
	if err := temporary.Close(); err != nil {
		return written, false, &ExtractionError{Path: item.relative, Stage: StageWrite, Err: err}
	}
	for {
		switch action {
		case "new":
			err = temporary.PublishNew(ctx)
			if errors.Is(err, ErrConflict) {
				var skip bool
				action, expected, skip, err = chooseConflict(ctx, destination, resolver, item.entry, item.relative)
				if err == nil && skip {
					return 0, true, nil
				}
				if err == nil {
					continue
				}
			}
		case operations.Replace:
			err = temporary.PublishReplace(ctx, expected.Token, expected.Identity)
		default:
			err = fmt.Errorf("%w: unsupported publication action %q", ErrConflict, action)
		}
		if err != nil {
			return written, false, &ExtractionError{Path: item.relative, Stage: StagePublish, Err: err}
		}
		published = true
		return written, false, nil
	}
}

func addChecked(current, addition, maximum uint64) (uint64, error) {
	if addition > maximum || current > maximum-addition || current > math.MaxUint64-addition {
		return current, fmt.Errorf("%w: byte limit %d", ErrLimitExceeded, maximum)
	}
	return current + addition, nil
}

func checkRatio(expanded uint64, compressed *int64, maximum uint64) error {
	if maximum == 0 {
		return ErrInvalidMetadata
	}
	if compressed == nil {
		return nil
	}
	if *compressed < 0 {
		return ErrInvalidMetadata
	}
	packed := uint64(*compressed)
	if packed == 0 {
		if expanded == 0 {
			return nil
		}
		return fmt.Errorf("%w: expansion ratio exceeds %d", ErrLimitExceeded, maximum)
	}
	if packed > math.MaxUint64/maximum {
		return nil
	}
	if expanded > packed*maximum {
		return fmt.Errorf("%w: expansion ratio exceeds %d", ErrLimitExceeded, maximum)
	}
	return nil
}

func failReport(report ExtractionReport, path string, stage ExtractionStage, err error) (ExtractionReport, error) {
	report.FailedPath, report.FailedStage = path, stage
	return report, &ExtractionError{Path: path, Stage: stage, Err: fmt.Errorf("%w: %w", ErrExtractionFailed, err)}
}
