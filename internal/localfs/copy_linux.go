//go:build linux

package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"golang.org/x/sys/unix"
)

func (LocalFileSystem) OpenCopyFile(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := check(ctx, "open-copy", path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open-copy", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, wrongKind(path, core.KindFile)
	}
	return &contextFile{ctx: ctx, file: file}, nil
}

func (LocalFileSystem) ReadCopyDirectory(ctx context.Context, path string) (entries []core.FileEntry, err error) {
	if err := check(ctx, "read-copy-dir", path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "read-copy-dir", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			entries = nil
		}
	}()
	for {
		if err := check(ctx, "read-copy-dir", path); err != nil {
			return nil, err
		}
		batch, readErr := file.ReadDir(128)
		for _, item := range batch {
			info, infoErr := item.Info()
			if infoErr != nil {
				return nil, pathError("read-copy-dir", childPath(path, item.Name()), infoErr)
			}
			entries = append(entries, entry(childPath(path, item.Name()), info))
		}
		if errors.Is(readErr, io.EOF) {
			return entries, nil
		}
		if readErr != nil {
			return nil, pathError("read-copy-dir", path, readErr)
		}
	}
}

func (l LocalFileSystem) PublishNew(ctx context.Context, temporary, destination string) error {
	if err := check(ctx, "publish", destination); err != nil {
		return err
	}
	if !temporaryInDestination(temporary, destination) {
		return fmt.Errorf("publish %q: %w: temporary file is outside destination directory", destination, fs.ErrInvalid)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, temporary, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return &fs.PathError{Op: "publish", Path: destination, Err: err}
	}
	return nil
}

func (l LocalFileSystem) PublishReplace(ctx context.Context, temporary, destination string, expected core.FileFingerprint) error {
	if err := check(ctx, "replace", destination); err != nil {
		return err
	}
	if !temporaryInDestination(temporary, destination) || expected.Token == "" || expected.Identity == "" {
		return fmt.Errorf("replace %q: %w", destination, fs.ErrInvalid)
	}
	current, err := l.Fingerprint(ctx, destination)
	if err != nil {
		return err
	}
	if current != expected {
		return &fs.PathError{Op: "replace", Path: destination, Err: core.ErrDestinationChanged}
	}
	if err := unix.Renameat2(unix.AT_FDCWD, temporary, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE); err != nil {
		return &fs.PathError{Op: "replace", Path: destination, Err: err}
	}
	displaced, inspectErr := l.Fingerprint(ctx, temporary)
	if inspectErr == nil && displaced.Identity == expected.Identity {
		return os.Remove(temporary)
	}
	// The destination changed between the precheck and the atomic exchange.
	// Exchange it back so the unapproved object remains at destination.
	rollbackErr := unix.Renameat2(unix.AT_FDCWD, temporary, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE)
	if rollbackErr != nil {
		return errors.Join(&fs.PathError{Op: "replace", Path: destination, Err: core.ErrDestinationChanged},
			fmt.Errorf("restore destination after concurrent change: %w", rollbackErr), core.ErrTemporaryHoldsDestination)
	}
	return &fs.PathError{Op: "replace", Path: destination, Err: core.ErrDestinationChanged}
}

func fingerprintTokens(info fs.FileInfo) (string, string, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", false
	}
	identity := fmt.Sprintf("%d:%d:%d:%d:%d", stat.Dev, stat.Ino, uint32(info.Mode()), info.Size(), info.ModTime().UnixNano())
	return fmt.Sprintf("%s:%d:%d", identity, stat.Ctim.Sec, stat.Ctim.Nsec), identity, true
}
