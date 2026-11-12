// Package core defines filesystem models and contracts independently of the GUI.
package core

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"
)

var (
	ErrDestinationChanged        = errors.New("destination changed after conflict resolution")
	ErrSourceChanged             = errors.New("source changed before it could be removed")
	ErrTemporaryHoldsDestination = errors.New("temporary path contains the displaced destination and must be preserved")
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
	Hidden     bool
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

// PathResolver is an optional capability for explicit navigation through links.
// Resolving a path must preserve the meaning of symlink/.. components.
type PathResolver interface {
	ResolvePath(ctx context.Context, path string) (string, error)
}

// DirectoryCreator is a narrow optional write capability. Name must be one
// provider-native path component; implementations must not create parents.
type DirectoryCreator interface {
	CreateDirectory(ctx context.Context, parent, name string) error
}

// TemporaryFile is a fully private regular file in the destination directory.
// Copy code closes it before publication; the implementation must keep Name
// on the same filesystem as the final destination.
type TemporaryFile interface {
	io.WriteCloser
	Sync() error
	Chmod(fs.FileMode) error
	Name() string
}

// FileFingerprint identifies the destination object that the user approved
// replacing. Token includes change data for the check immediately before
// publication; Identity remains stable across an atomic directory rename.
type FileFingerprint struct {
	Token    string
	Identity string
}

// CopySource opens a previously classified regular file or directory without
// following a final symlink. Platforms unable to guarantee this must fail
// explicitly rather than silently dereference it.
type CopySource interface {
	OpenCopyFile(ctx context.Context, path string) (io.ReadCloser, error)
	ReadCopyDirectory(ctx context.Context, path string) ([]FileEntry, error)
}

// CopyDestination is the narrow mutation capability needed by the copy use
// case. PublishNew must never overwrite. PublishReplace must either replace
// exactly expected or leave the current destination intact.
type CopyDestination interface {
	CreateTemporaryFile(ctx context.Context, directory string, mode fs.FileMode) (TemporaryFile, error)
	CreateTemporarySymlink(ctx context.Context, directory, target string) (string, error)
	CreateDirectoryPath(ctx context.Context, path string, mode fs.FileMode) error
	Fingerprint(ctx context.Context, path string) (FileFingerprint, error)
	PublishNew(ctx context.Context, temporary, destination string) error
	PublishReplace(ctx context.Context, temporary, destination string, expected FileFingerprint) error
	RemoveTemporary(ctx context.Context, path string) error
}

// MoveFileSystem contains the narrow atomic mutations needed by the move use
// case. Implementations must never overwrite an unapproved destination and
// must condition source removal on the captured fingerprint.
type MoveFileSystem interface {
	RenameNew(ctx context.Context, source, destination string, expected FileFingerprint) error
	RenameReplace(ctx context.Context, source, destination string, expectedSource, expectedDestination FileFingerprint) error
	RemoveSource(ctx context.Context, source string, expected FileFingerprint) error
}
