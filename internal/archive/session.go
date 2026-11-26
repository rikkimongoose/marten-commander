package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type EntryKind string

const (
	EntryFile      EntryKind = "file"
	EntryDirectory EntryKind = "directory"
	EntrySymlink   EntryKind = "symlink"
	EntryHardlink  EntryKind = "hardlink"
	EntrySpecial   EntryKind = "special"
)

// ArchiveEntry contains format-independent metadata. Path is the canonical
// virtual path; RawPath preserves the driver-supplied name for extraction
// safety checks. Nil sizes or ModifiedAt mean the format omitted that value.
// LinkTarget is diagnostic only: extraction never follows or creates links.
type ArchiveEntry struct {
	Path           string
	RawPath        string
	Name           string
	Kind           EntryKind
	Size           *int64
	CompressedSize *int64
	ModifiedAt     *time.Time
	LinkTarget     string
	Extra          map[string]string
}

// ArchiveSession is a read-only virtual filesystem. Callers must close every
// opened stream, and must eventually close the session. Closing the session
// also closes streams that callers left open.
type ArchiveSession interface {
	Format() Format
	ReadDir(context.Context, string) ([]ArchiveEntry, error)
	Stat(context.Context, string) (ArchiveEntry, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Close() error
}

type session struct {
	mu        sync.Mutex
	format    Format
	input     Input
	backend   BackendSession
	entries   map[string]ArchiveEntry
	children  map[string][]string
	streams   map[*sessionReader]struct{}
	closed    bool
	closeErr  error
	closeDone chan struct{}
}

func newSession(ctx context.Context, format Format, input Input, backend BackendSession) (*session, error) {
	raw, err := backend.Entries(ctx)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, readError("list entries", err)
	}
	result := &session{format: format, input: input, backend: backend, streams: make(map[*sessionReader]struct{})}
	if err := result.buildIndex(ctx, raw); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *session) Format() Format { return s.format }

func (s *session) buildIndex(ctx context.Context, raw []ArchiveEntry) error {
	s.entries = map[string]ArchiveEntry{"/": {Path: "/", Name: "/", Kind: EntryDirectory}}
	explicit := make(map[string]bool)
	for _, item := range raw {
		if err := contextError(ctx); err != nil {
			return err
		}
		name, err := NormalizePath(item.Path)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrCorruptArchive, err)
		}
		if item.Kind != EntryFile && item.Kind != EntryDirectory && item.Kind != EntrySymlink &&
			item.Kind != EntryHardlink && item.Kind != EntrySpecial {
			return pathError(item.Path, fmt.Errorf("%w: kind %q", ErrUnsupportedEntry, item.Kind))
		}
		if name == "/" {
			if item.Kind != EntryDirectory {
				return fmt.Errorf("%w: root is not a directory", ErrCorruptArchive)
			}
			continue
		}
		base := path.Base(name)
		if item.Name != "" && item.Name != base {
			return fmt.Errorf("%w: entry name %q does not match path %q", ErrCorruptArchive, item.Name, item.Path)
		}
		if explicit[name] {
			return fmt.Errorf("%w: duplicate entry %q", ErrCorruptArchive, name)
		}
		if existing, exists := s.entries[name]; exists && existing.Kind != item.Kind {
			return fmt.Errorf("%w: file/directory conflict at %q", ErrCorruptArchive, name)
		}
		for parent := path.Dir(name); parent != "/"; parent = path.Dir(parent) {
			if existing, exists := s.entries[parent]; exists && existing.Kind != EntryDirectory {
				return fmt.Errorf("%w: file used as parent at %q", ErrCorruptArchive, parent)
			}
			s.entries[parent] = ArchiveEntry{Path: parent, RawPath: strings.TrimPrefix(parent, "/"), Name: path.Base(parent), Kind: EntryDirectory}
		}
		rawPath := item.Path
		item.Path, item.RawPath, item.Name, item.Extra = name, rawPath, base, cloneMap(item.Extra)
		if item.Size != nil {
			size := *item.Size
			item.Size = &size
		}
		if item.ModifiedAt != nil {
			modified := *item.ModifiedAt
			item.ModifiedAt = &modified
		}
		if item.CompressedSize != nil {
			compressed := *item.CompressedSize
			item.CompressedSize = &compressed
		}
		s.entries[name] = item
		explicit[name] = true
	}
	s.children = make(map[string][]string)
	for name := range s.entries {
		if name != "/" {
			parent := path.Dir(name)
			s.children[parent] = append(s.children[parent], name)
		}
	}
	for parent := range s.children {
		sort.Slice(s.children[parent], func(i, j int) bool {
			return s.entries[s.children[parent][i]].Name < s.entries[s.children[parent][j]].Name
		})
	}
	return nil
}

func (s *session) ReadDir(ctx context.Context, name string) ([]ArchiveEntry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	canonical, err := NormalizePath(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	entry, exists := s.entries[canonical]
	if !exists {
		return nil, pathError(canonical, ErrEntryNotFound)
	}
	if entry.Kind != EntryDirectory {
		return nil, pathError(canonical, ErrNotDirectory)
	}
	names := s.children[canonical]
	result := make([]ArchiveEntry, 0, len(names))
	for _, child := range names {
		result = append(result, cloneEntry(s.entries[child]))
	}
	return result, nil
}

func (s *session) Stat(ctx context.Context, name string) (ArchiveEntry, error) {
	if err := contextError(ctx); err != nil {
		return ArchiveEntry{}, err
	}
	canonical, err := NormalizePath(name)
	if err != nil {
		return ArchiveEntry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ArchiveEntry{}, ErrClosed
	}
	entry, exists := s.entries[canonical]
	if !exists {
		return ArchiveEntry{}, pathError(canonical, ErrEntryNotFound)
	}
	return cloneEntry(entry), nil
}

func (s *session) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	canonical, err := NormalizePath(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	entry, exists := s.entries[canonical]
	if !exists {
		s.mu.Unlock()
		return nil, pathError(canonical, ErrEntryNotFound)
	}
	if entry.Kind != EntryFile {
		s.mu.Unlock()
		if entry.Kind != EntryDirectory {
			return nil, pathError(canonical, ErrUnsupportedEntry)
		}
		return nil, pathError(canonical, ErrNotFile)
	}
	reader, err := s.backend.Open(ctx, canonical)
	if err != nil {
		s.mu.Unlock()
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, readError("open "+canonical, err)
	}
	if reader == nil {
		s.mu.Unlock()
		return nil, readError("open "+canonical, errors.New("backend returned nil stream"))
	}
	wrapped := &sessionReader{ctx: ctx, reader: reader, owner: s}
	s.streams[wrapped] = struct{}{}
	wrapped.stopCancellation = context.AfterFunc(ctx, func() { _ = wrapped.Close() })
	s.mu.Unlock()
	return wrapped, nil
}

func (s *session) Close() error {
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	s.closed = true
	s.closeDone = make(chan struct{})
	streams := make([]*sessionReader, 0, len(s.streams))
	for stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mu.Unlock()

	var errs []error
	for _, stream := range streams {
		if err := stream.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.backend.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := s.input.Close(); err != nil {
		errs = append(errs, err)
	}
	s.mu.Lock()
	s.closeErr = errors.Join(errs...)
	err := s.closeErr
	close(s.closeDone)
	s.mu.Unlock()
	return err
}

func (s *session) release(reader *sessionReader) {
	s.mu.Lock()
	delete(s.streams, reader)
	s.mu.Unlock()
}

type sessionReader struct {
	mu               sync.Mutex
	ctx              context.Context
	reader           io.ReadCloser
	owner            *session
	stopCancellation func() bool
	closed           bool
	closeErr         error
	closeDone        chan struct{}
}

func (r *sessionReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, ErrClosed
	}
	reader := r.reader
	r.mu.Unlock()
	n, err := reader.Read(buffer)
	if contextErr := r.ctx.Err(); contextErr != nil {
		return n, contextErr
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, readError("read entry stream", err)
	}
	return n, err
}

func (r *sessionReader) Close() error {
	r.mu.Lock()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		<-done
		r.mu.Lock()
		err := r.closeErr
		r.mu.Unlock()
		return err
	}
	r.closed = true
	r.closeDone = make(chan struct{})
	stop := r.stopCancellation
	reader := r.reader
	r.mu.Unlock()
	err := reader.Close()
	if stop != nil {
		stop()
	}
	r.owner.release(r)
	r.mu.Lock()
	r.closeErr = err
	close(r.closeDone)
	r.mu.Unlock()
	return err
}

func cloneEntry(entry ArchiveEntry) ArchiveEntry {
	entry.Extra = cloneMap(entry.Extra)
	if entry.Size != nil {
		size := *entry.Size
		entry.Size = &size
	}
	if entry.ModifiedAt != nil {
		modified := *entry.ModifiedAt
		entry.ModifiedAt = &modified
	}
	if entry.CompressedSize != nil {
		compressed := *entry.CompressedSize
		entry.CompressedSize = &compressed
	}
	return entry
}

func cloneMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
