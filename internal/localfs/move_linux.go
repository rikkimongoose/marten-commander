//go:build linux

package localfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"golang.org/x/sys/unix"
)

var _ core.MoveFileSystem = LocalFileSystem{}

func (l LocalFileSystem) RenameNew(ctx context.Context, source, destination string, expected core.FileFingerprint) error {
	if err := check(ctx, "move", source); err != nil {
		return err
	}
	if expected.Identity == "" || expected.Token == "" {
		return fmt.Errorf("move %q: %w", source, fs.ErrInvalid)
	}
	current, err := l.Fingerprint(ctx, source)
	if err != nil {
		return err
	}
	if current.Identity != expected.Identity {
		return &fs.PathError{Op: "move", Path: source, Err: core.ErrSourceChanged}
	}
	if err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return &fs.PathError{Op: "move", Path: destination, Err: err}
	}
	moved, err := l.Fingerprint(context.WithoutCancel(ctx), destination)
	if err == nil && moved.Identity == expected.Identity {
		return nil
	}
	rollback := unix.Renameat2(unix.AT_FDCWD, destination, unix.AT_FDCWD, source, unix.RENAME_NOREPLACE)
	return errors.Join(&fs.PathError{Op: "move", Path: source, Err: core.ErrSourceChanged}, rollback)
}

func (l LocalFileSystem) RenameReplace(ctx context.Context, source, destination string, expectedSource, expectedDestination core.FileFingerprint) error {
	if err := check(ctx, "move-replace", source); err != nil {
		return err
	}
	if expectedSource.Identity == "" || expectedDestination.Identity == "" {
		return fmt.Errorf("move-replace: %w", fs.ErrInvalid)
	}
	currentSource, err := l.Fingerprint(ctx, source)
	if err != nil {
		return err
	}
	currentDestination, err := l.Fingerprint(ctx, destination)
	if err != nil {
		return err
	}
	if currentSource != expectedSource {
		return &fs.PathError{Op: "move-replace", Path: source, Err: core.ErrSourceChanged}
	}
	if currentDestination != expectedDestination {
		return &fs.PathError{Op: "move-replace", Path: destination, Err: core.ErrDestinationChanged}
	}
	if err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE); err != nil {
		return &fs.PathError{Op: "move-replace", Path: destination, Err: err}
	}
	moved, movedErr := l.Fingerprint(context.WithoutCancel(ctx), destination)
	displaced, displacedErr := l.Fingerprint(context.WithoutCancel(ctx), source)
	if movedErr == nil && displacedErr == nil && moved.Identity == expectedSource.Identity && displaced.Identity == expectedDestination.Identity {
		if err := l.RemoveSource(context.WithoutCancel(ctx), source, displaced); err != nil {
			return errors.Join(err, core.ErrTemporaryHoldsDestination)
		}
		return nil
	}
	rollback := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE)
	return errors.Join(&fs.PathError{Op: "move-replace", Path: destination, Err: core.ErrDestinationChanged}, movedErr, displacedErr, rollback)
}

func (l LocalFileSystem) RemoveSource(ctx context.Context, source string, expected core.FileFingerprint) error {
	if err := check(ctx, "remove-source", source); err != nil {
		return err
	}
	current, err := l.Fingerprint(ctx, source)
	if err != nil {
		return err
	}
	if current != expected {
		return &fs.PathError{Op: "remove-source", Path: source, Err: core.ErrSourceChanged}
	}
	directory := filepath.Dir(source)
	for attempts := 0; attempts < 16; attempts++ {
		placeholder, createErr := os.CreateTemp(directory, ".marten-move-source-*")
		if createErr != nil {
			return createErr
		}
		quarantine := placeholder.Name()
		closeErr := placeholder.Close()
		removeErr := os.Remove(quarantine)
		if closeErr != nil || removeErr != nil {
			return errors.Join(closeErr, removeErr)
		}
		if renameErr := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, quarantine, unix.RENAME_NOREPLACE); renameErr != nil {
			if errors.Is(renameErr, unix.EEXIST) {
				continue
			}
			return &fs.PathError{Op: "remove-source", Path: source, Err: renameErr}
		}
		moved, inspectErr := l.Fingerprint(context.WithoutCancel(ctx), quarantine)
		if inspectErr == nil && moved.Identity == expected.Identity {
			if removeErr := os.Remove(quarantine); removeErr != nil {
				return &fs.PathError{Op: "remove-source", Path: source, Err: removeErr}
			}
			return nil
		}
		rollback := unix.Renameat2(unix.AT_FDCWD, quarantine, unix.AT_FDCWD, source, unix.RENAME_NOREPLACE)
		return errors.Join(&fs.PathError{Op: "remove-source", Path: source, Err: core.ErrSourceChanged}, inspectErr, rollback)
	}
	return fmt.Errorf("remove-source %q: could not reserve quarantine name", source)
}
