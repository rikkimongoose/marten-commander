//go:build linux

package archive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"golang.org/x/sys/unix"
)

// LocalExtractionDestination anchors every operation to an open directory
// descriptor. Path components are opened with O_NOFOLLOW and publication uses
// renameat2, so no operation resolves through a destination symlink.
type LocalExtractionDestination struct {
	mu     sync.Mutex
	rootFD int
	closed bool
}

func NewLocalExtractionDestination(ctx context.Context, root string) (*LocalExtractionDestination, error) {
	if ctx == nil {
		panic("archive: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) || strings.ContainsRune(root, 0) {
		return nil, fmt.Errorf("%w: extraction root must be an absolute local path", ErrUnsafeDestination)
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open extraction root %q: %w", ErrUnsafeDestination, root, err)
	}
	return &LocalExtractionDestination{rootFD: fd}, nil
}

func (d *LocalExtractionDestination) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return unix.Close(d.rootFD)
}

func (d *LocalExtractionDestination) root(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return -1, ErrClosed
	}
	fd, err := unix.FcntlInt(uintptr(d.rootFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	return fd, nil
}

func (d *LocalExtractionDestination) EnsureDirectory(ctx context.Context, name string) error {
	if _, err := ValidateExtractionPath(name); err != nil {
		return err
	}
	current, err := d.root(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(current) }()
	for _, component := range strings.Split(name, "/") {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, openErr := openDirectoryAt(current, component)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, component, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return &fs.PathError{Op: "mkdirat", Path: name, Err: mkdirErr}
			}
			next, openErr = openDirectoryAt(current, component)
		}
		if openErr != nil {
			return destinationPathError("open directory", name, openErr)
		}
		_ = unix.Close(current)
		current = next
	}
	return nil
}

func (d *LocalExtractionDestination) Inspect(ctx context.Context, name string) (DestinationEntry, error) {
	parent, base, err := d.openParent(ctx, name)
	if err != nil {
		return DestinationEntry{}, err
	}
	defer unix.Close(parent)
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, base, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return DestinationEntry{}, pathError(name, ErrEntryNotFound)
		}
		return DestinationEntry{}, destinationPathError("inspect", name, err)
	}
	return destinationEntry(stat), nil
}

func (d *LocalExtractionDestination) CreateTemporary(ctx context.Context, name string) (ExtractionFile, error) {
	parent, base, err := d.openParent(ctx, name)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 32; attempt++ {
		temporary, randomErr := temporaryName()
		if randomErr != nil {
			_ = unix.Close(parent)
			return nil, randomErr
		}
		fd, openErr := unix.Openat(parent, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(openErr, unix.EEXIST) {
			continue
		}
		if openErr != nil {
			_ = unix.Close(parent)
			return nil, &fs.PathError{Op: "create extraction temporary", Path: name, Err: openErr}
		}
		return &localExtractionFile{file: os.NewFile(uintptr(fd), temporary), directoryFD: parent,
			temporary: temporary, destination: base}, nil
	}
	_ = unix.Close(parent)
	return nil, fmt.Errorf("create extraction temporary: %w", fs.ErrExist)
}

func (d *LocalExtractionDestination) openParent(ctx context.Context, name string) (int, string, error) {
	validated, err := ValidateExtractionPath(name)
	if err != nil {
		return -1, "", err
	}
	parts := strings.Split(validated, "/")
	current, err := d.root(ctx)
	if err != nil {
		return -1, "", err
	}
	for _, component := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(current)
			return -1, "", err
		}
		next, openErr := openDirectoryAt(current, component)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, "", destinationPathError("open parent", name, openErr)
		}
		current = next
	}
	return current, parts[len(parts)-1], nil
}

func openDirectoryAt(parent int, name string) (int, error) {
	return unix.Openat2(parent, name, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
}

func destinationPathError(operation, name string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOSYS) {
		return pathError(name, fmt.Errorf("%w: %s: %w", ErrUnsafeDestination, operation, err))
	}
	if errors.Is(err, unix.ENOENT) {
		return pathError(name, ErrEntryNotFound)
	}
	return &fs.PathError{Op: operation, Path: name, Err: err}
}

func destinationEntry(stat unix.Stat_t) DestinationEntry {
	kind := DestinationOther
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = DestinationFile
	case unix.S_IFDIR:
		kind = DestinationDirectory
	case unix.S_IFLNK:
		kind = DestinationSymlink
	}
	return DestinationEntry{Kind: kind, Token: statToken(stat), Identity: statIdentity(stat)}
}

func statToken(stat unix.Stat_t) string {
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Mode, stat.Size,
		stat.Mtim.Sec, stat.Mtim.Nsec, stat.Ctim.Sec, stat.Ctim.Nsec)
}

func statIdentity(stat unix.Stat_t) string {
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Mode, stat.Size, stat.Mtim.Sec, stat.Mtim.Nsec)
}

func temporaryName() (string, error) {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return ".marten-extract-" + hex.EncodeToString(data[:]), nil
}

type localExtractionFile struct {
	mu          sync.Mutex
	file        *os.File
	directoryFD int
	temporary   string
	destination string
	closed      bool
	consumed    bool
	preserve    bool
	closeErr    error
}

func (f *localExtractionFile) Write(data []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.consumed {
		return 0, ErrClosed
	}
	return f.file.Write(data)
}

func (f *localExtractionFile) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.consumed {
		return ErrClosed
	}
	return f.file.Sync()
}

func (f *localExtractionFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return f.closeErr
	}
	f.closed = true
	f.closeErr = f.file.Close()
	return f.closeErr
}

func (f *localExtractionFile) PublishNew(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed || f.consumed {
		return ErrClosed
	}
	err := unix.Renameat2(f.directoryFD, f.temporary, f.directoryFD, f.destination, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("%w: destination exists", ErrConflict)
	}
	if err != nil {
		return &fs.PathError{Op: "publish extraction", Path: f.destination, Err: err}
	}
	f.consumed = true
	_ = f.closeDirectory()
	return nil
}

func (f *localExtractionFile) PublishReplace(ctx context.Context, expectedToken, expectedIdentity string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if expectedToken == "" || expectedIdentity == "" {
		return fmt.Errorf("%w: missing destination identity", ErrConflict)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed || f.consumed {
		return ErrClosed
	}
	var before unix.Stat_t
	if err := unix.Fstatat(f.directoryFD, f.destination, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: destination changed: %w", ErrConflict, err)
	}
	if destinationEntry(before).Kind != DestinationFile || statToken(before) != expectedToken {
		return fmt.Errorf("%w: destination changed", ErrConflict)
	}
	if err := unix.Renameat2(f.directoryFD, f.temporary, f.directoryFD, f.destination, unix.RENAME_EXCHANGE); err != nil {
		return &fs.PathError{Op: "replace extraction", Path: f.destination, Err: err}
	}
	var displaced unix.Stat_t
	inspectErr := unix.Fstatat(f.directoryFD, f.temporary, &displaced, unix.AT_SYMLINK_NOFOLLOW)
	if inspectErr == nil && destinationEntry(displaced).Kind == DestinationFile && statIdentity(displaced) == expectedIdentity {
		if err := unix.Unlinkat(f.directoryFD, f.temporary, 0); err != nil {
			rollbackErr := unix.Renameat2(f.directoryFD, f.temporary, f.directoryFD, f.destination, unix.RENAME_EXCHANGE)
			if rollbackErr != nil {
				f.preserve = true
				return errors.Join(&fs.PathError{Op: "remove replaced destination", Path: f.destination, Err: err},
					core.ErrTemporaryHoldsDestination,
					fmt.Errorf("restore destination: %w; displaced destination is preserved as %q", rollbackErr, f.temporary))
			}
			return &fs.PathError{Op: "remove replaced destination", Path: f.destination, Err: err}
		}
		f.consumed = true
		_ = f.closeDirectory()
		return nil
	}
	rollbackErr := unix.Renameat2(f.directoryFD, f.temporary, f.directoryFD, f.destination, unix.RENAME_EXCHANGE)
	if rollbackErr != nil {
		f.preserve = true
		return errors.Join(fmt.Errorf("%w: destination changed", ErrConflict), core.ErrTemporaryHoldsDestination,
			fmt.Errorf("restore destination: %w; displaced destination is preserved as %q", rollbackErr, f.temporary))
	}
	return fmt.Errorf("%w: destination changed", ErrConflict)
}

func (f *localExtractionFile) Discard(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.consumed {
		return nil
	}
	if !f.closed {
		f.closed = true
		f.closeErr = f.file.Close()
	}
	var removeErr error
	if !f.preserve {
		removeErr = unix.Unlinkat(f.directoryFD, f.temporary, 0)
		if errors.Is(removeErr, unix.ENOENT) {
			removeErr = nil
		}
	}
	f.consumed = true
	return errors.Join(f.closeErr, removeErr, f.closeDirectory())
}

func (f *localExtractionFile) closeDirectory() error {
	if f.directoryFD < 0 {
		return nil
	}
	err := unix.Close(f.directoryFD)
	f.directoryFD = -1
	return err
}
