package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Format is a stable driver-defined archive format identifier, for example
// "zip" or "tar.gz".
type Format string

// Match describes how confidently a driver recognized an input. PossibleMatch
// supports formats such as TAR that may need structural validation rather than
// one unique magic value.
type Match uint8

const (
	NoMatch Match = iota
	PossibleMatch
	DefiniteMatch
)

// Input is seek-independent archive data used by probes and a selected driver.
// Implementations must allow bounded ReadAt calls and report the total size.
// Source owns and closes the input after a failed Open or when its session ends.
type Input interface {
	io.ReaderAt
	Size() int64
	io.Closer
}

// InputOpener opens an archive location without assigning local filesystem
// semantics to the location string.
type InputOpener interface {
	OpenArchive(context.Context, string) (Input, error)
}

type InputOpenerFunc func(context.Context, string) (Input, error)

func (f InputOpenerFunc) OpenArchive(ctx context.Context, location string) (Input, error) {
	return f(ctx, location)
}

// BackendSession is implemented by a concrete archive format driver. Entries
// may be relative or absolute POSIX paths. The common session validates them,
// creates implicit directories and owns every stream returned by Open.
type BackendSession interface {
	Entries(context.Context) ([]ArchiveEntry, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Close() error
}

// Driver detects and opens one archive format. Probe must use bounded ReadAt
// calls and must not close input. Open likewise borrows input for the lifetime
// of the returned backend session.
type Driver interface {
	Format() Format
	Probe(context.Context, Input) (Match, error)
	Open(context.Context, Input) (BackendSession, error)
}

// Registry is a concurrency-safe ordered set of format drivers. A format can
// be registered once. When confidence is equal, earlier registration wins.
type Registry struct {
	mu      sync.RWMutex
	drivers []Driver
	formats map[Format]struct{}
}

func NewRegistry() *Registry { return &Registry{formats: make(map[Format]struct{})} }

func (r *Registry) Register(driver Driver) error {
	if driver == nil || driver.Format() == "" {
		return fmt.Errorf("register archive driver: %w", ErrUnsupportedFormat)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	format := driver.Format()
	if _, exists := r.formats[format]; exists {
		return fmt.Errorf("%w: %s", ErrFormatRegistered, format)
	}
	r.formats[format] = struct{}{}
	r.drivers = append(r.drivers, driver)
	return nil
}

func (r *Registry) snapshot() []Driver {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Driver(nil), r.drivers...)
}

// ArchiveSource opens format-independent read-only archive sessions.
type ArchiveSource interface {
	Open(context.Context, string) (ArchiveSession, error)
}

// Source detects registered formats and constructs the common virtual
// filesystem session.
type Source struct {
	opener   InputOpener
	registry *Registry
}

func NewSource(opener InputOpener, registry *Registry) *Source {
	return &Source{opener: opener, registry: registry}
}

func (s *Source) Open(ctx context.Context, location string) (ArchiveSession, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.opener == nil {
		return nil, fmt.Errorf("%w: no input opener", ErrOpenArchive)
	}
	input, err := s.opener.OpenArchive(ctx, location)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("%w: %w", ErrOpenArchive, err)
	}
	if input == nil {
		return nil, fmt.Errorf("%w: input opener returned nil", ErrOpenArchive)
	}
	keepInput := false
	defer func() {
		if !keepInput {
			_ = input.Close()
		}
	}()

	driver, err := detect(ctx, input, s.registry.snapshot())
	if err != nil {
		return nil, err
	}
	backend, err := driver.Open(ctx, input)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		if errors.Is(err, ErrCorruptArchive) || errors.Is(err, ErrEncrypted) ||
			errors.Is(err, ErrUnsupportedEntry) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrOpenArchive, driver.Format(), err)
	}
	if backend == nil {
		return nil, fmt.Errorf("%w: %s driver returned nil session", ErrOpenArchive, driver.Format())
	}
	session, err := newSession(ctx, driver.Format(), input, backend)
	if err != nil {
		_ = backend.Close()
		return nil, err
	}
	keepInput = true
	return session, nil
}

func detect(ctx context.Context, input Input, drivers []Driver) (Driver, error) {
	var selected Driver
	best := NoMatch
	for _, driver := range drivers {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		match, err := driver.Probe(ctx, input)
		if err != nil {
			if contextErr := contextError(ctx); contextErr != nil {
				return nil, contextErr
			}
			return nil, readError("detect "+string(driver.Format()), err)
		}
		if match > DefiniteMatch {
			return nil, fmt.Errorf("%w: invalid probe result from %s", ErrRead, driver.Format())
		}
		if match > best {
			selected, best = driver, match
		}
	}
	if selected == nil {
		return nil, ErrUnsupportedFormat
	}
	return selected, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		panic("archive: nil context")
	}
	return ctx.Err()
}
