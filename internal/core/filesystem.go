// Package core defines filesystem models and contracts independently of the GUI.
package core

import (
	"context"
	"io"
	"io/fs"
	"time"
)

// EntryKind describes the entry itself, without following symbolic links.
type EntryKind string

const (
	KindFile      EntryKind = "file"
	KindDirectory EntryKind = "directory"
	KindSymlink   EntryKind = "symlink"
	KindOther     EntryKind = "other"
)

// KindFromMode classifies regular files, directories, links and special files.
func KindFromMode(mode fs.FileMode) EntryKind {
	switch {
	case mode&fs.ModeSymlink != 0:
		return KindSymlink
	case mode.IsDir():
		return KindDirectory
	case mode.IsRegular():
		return KindFile
	default:
		return KindOther
	}
}

// FileEntry is metadata for an entry, not necessarily its symlink target.
// Path is a provider-native address. Name and Path may contain non-UTF-8 bytes;
// neither is a presentation string or a JSON-safe identifier.
type FileEntry struct {
	Path       string
	Name       string
	Kind       EntryKind
	Size       int64 // Provider-reported size; only regular file sizes are comparable.
	ModifiedAt time.Time
	Mode       fs.FileMode
}

// FileSystem is the minimum read contract. Writing and watching are separate
// capabilities, to be added when their consumers are implemented.
// Methods preserve error causes for errors.Is/errors.As. Contexts must be non-nil.
type FileSystem interface {
	// ReadDir returns an unsorted listing, with metadata for links themselves.
	// On failure it returns no partial listing. It is not a filesystem snapshot.
	ReadDir(ctx context.Context, path string) ([]FileEntry, error)
	// Lstat reads an entry's metadata without following its final symlink.
	Lstat(ctx context.Context, path string) (FileEntry, error)
	// Open opens a regular file, following symlinks. The caller must close it,
	// including after cancellation. Context cancellation also applies to reads.
	Open(ctx context.Context, path string) (io.ReadCloser, error)
}

// LinkReader is an optional capability. The returned target is the literal link
// contents, possibly relative or dangling; callers must not silently resolve it.
type LinkReader interface {
	Readlink(ctx context.Context, path string) (string, error)
}
