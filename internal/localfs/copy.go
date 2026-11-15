package localfs

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

var _ core.CopyDestination = LocalFileSystem{}
var _ core.CopySource = LocalFileSystem{}

func (LocalFileSystem) CreateTemporaryFile(ctx context.Context, directory string, mode fs.FileMode) (core.TemporaryFile, error) {
	if err := check(ctx, "create-temp", directory); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(directory, ".marten-copy-*")
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	return file, nil
}

func (LocalFileSystem) CreateTemporarySymlink(ctx context.Context, directory, target string) (string, error) {
	if err := check(ctx, "create-temp-link", directory); err != nil {
		return "", err
	}
	for attempts := 0; attempts < 16; attempts++ {
		placeholder, err := os.CreateTemp(directory, ".marten-copy-link-*")
		if err != nil {
			return "", err
		}
		name := placeholder.Name()
		if err := placeholder.Close(); err != nil {
			_ = os.Remove(name)
			return "", err
		}
		if err := os.Remove(name); err != nil {
			return "", err
		}
		if err := os.Symlink(target, name); err == nil {
			return name, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("create temporary symbolic link: %w", fs.ErrExist)
}

func (LocalFileSystem) CreateDirectoryPath(ctx context.Context, path string, mode fs.FileMode) error {
	if err := check(ctx, "mkdir", path); err != nil {
		return err
	}
	return os.Mkdir(path, mode.Perm())
}

func (LocalFileSystem) Fingerprint(ctx context.Context, path string) (core.FileFingerprint, error) {
	if err := check(ctx, "fingerprint", path); err != nil {
		return core.FileFingerprint{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return core.FileFingerprint{}, err
	}
	token, identity, ok := fingerprintTokens(info)
	if !ok {
		return core.FileFingerprint{}, fmt.Errorf("fingerprint %q: %w", path, errorsUnsupportedIdentity)
	}
	return core.FileFingerprint{Token: token, Identity: identity}, nil
}

func (LocalFileSystem) RemoveTemporary(ctx context.Context, path string) error {
	if err := check(ctx, "remove-temp", path); err != nil {
		return err
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func temporaryInDestination(temporary, destination string) bool {
	return filepath.Dir(temporary) == filepath.Dir(destination) && filepath.Base(temporary) != filepath.Base(destination)
}

var errorsUnsupportedIdentity = fmt.Errorf("stable local file identity is unavailable")
