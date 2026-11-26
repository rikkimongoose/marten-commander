package archive

import (
	"errors"
	"fmt"
)

var (
	ErrUnsupportedFormat = errors.New("archive: unsupported format")
	ErrCorruptArchive    = errors.New("archive: corrupt archive")
	ErrOpenArchive       = errors.New("archive: cannot open archive")
	ErrEntryNotFound     = errors.New("archive: entry not found")
	ErrNotDirectory      = errors.New("archive: entry is not a directory")
	ErrNotFile           = errors.New("archive: entry is not a file")
	ErrUnsafePath        = errors.New("archive: unsafe path")
	ErrEncrypted         = errors.New("archive: encrypted entry")
	ErrUnsupportedEntry  = errors.New("archive: unsupported entry")
	ErrRead              = errors.New("archive: read failed")
	ErrClosed            = errors.New("archive: session closed")
	ErrFormatRegistered  = errors.New("archive: format already registered")
	ErrLimitExceeded     = errors.New("archive: extraction limit exceeded")
	ErrInvalidMetadata   = errors.New("archive: invalid entry metadata")
	ErrConflict          = errors.New("archive: destination conflict")
	ErrUnsafeDestination = errors.New("archive: unsafe extraction destination")
	ErrExtractionFailed  = errors.New("archive: extraction failed")
)

// PathError identifies the archive path rejected by an operation.
type PathError struct {
	Path string
	Err  error
}

func (e *PathError) Error() string { return fmt.Sprintf("archive path %q: %v", e.Path, e.Err) }
func (e *PathError) Unwrap() error { return e.Err }

func pathError(name string, err error) error { return &PathError{Path: name, Err: err} }

func readError(operation string, err error) error {
	if err == nil || errors.Is(err, ErrRead) || errors.Is(err, ErrCorruptArchive) ||
		errors.Is(err, ErrEncrypted) || errors.Is(err, ErrUnsupportedEntry) {
		return err
	}
	return fmt.Errorf("%w: %s: %w", ErrRead, operation, err)
}
