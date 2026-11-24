package sftp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// remoteClient is the read-only part of pkg/sftp.Client used by directory
// browsing. Keeping it narrow makes navigation testable without an SSH server.
type remoteClient interface {
	Getwd() (string, error)
	ReadDirContext(context.Context, string) ([]os.FileInfo, error)
	Stat(string) (os.FileInfo, error)
}

type RemoteEntry struct {
	Path       string
	Name       string
	Kind       string
	Size       int64
	ModifiedAt time.Time
	Hidden     bool
	Navigable  bool
	Detail     string
}

type RemoteDirectory struct {
	Path    string
	Parent  string
	Entries []RemoteEntry
}

// ReadDirectory uses an already authenticated session. The connection ID is
// session-specific, so reconnecting cannot redirect a request for an old
// transport to the new session.
func (m *Manager) ReadDirectory(ctx context.Context, connectionID, requested string) (RemoteDirectory, error) {
	if err := ctx.Err(); err != nil {
		return RemoteDirectory{}, err
	}
	m.mu.Lock()
	current := m.sessions[connectionID]
	m.mu.Unlock()
	if current == nil {
		return RemoteDirectory{}, errors.New("SFTP-соединение закрыто")
	}
	readCtx := ctx
	cancel := func() {}
	if current.timeout > 0 {
		readCtx, cancel = context.WithTimeout(ctx, current.timeout)
	}
	defer cancel()

	directory := requested
	if directory == "" {
		directory = current.initialPath
		if directory == "" {
			var err error
			directory, err = remoteGetwd(readCtx, current.remote)
			if err != nil {
				return RemoteDirectory{}, remoteReadError(readCtx, "не удалось определить рабочий каталог", err)
			}
		}
	}
	if !path.IsAbs(directory) || strings.ContainsRune(directory, 0) {
		return RemoteDirectory{}, fs.ErrInvalid
	}
	directory = path.Clean(directory)
	entries, err := current.remote.ReadDirContext(readCtx, directory)
	if err != nil {
		return RemoteDirectory{}, remoteReadError(readCtx, fmt.Sprintf("не удалось прочитать каталог %q", directory), err)
	}
	result := RemoteDirectory{Path: directory, Parent: path.Dir(directory), Entries: make([]RemoteEntry, 0, len(entries))}
	for _, info := range entries {
		if err := readCtx.Err(); err != nil {
			return RemoteDirectory{}, remoteReadError(readCtx, "чтение каталога прервано", err)
		}
		name := info.Name()
		if name == "." || name == ".." || strings.Contains(name, "/") || strings.ContainsRune(name, 0) {
			continue
		}
		item := RemoteEntry{Path: path.Join(directory, name), Name: name, Size: info.Size(),
			ModifiedAt: info.ModTime(), Hidden: strings.HasPrefix(name, ".")}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			item.Kind = "symlink"
			target, statErr := remoteStat(readCtx, current.remote, item.Path)
			item.Navigable = statErr == nil && target.IsDir()
			if statErr != nil {
				item.Detail = statErr.Error()
			}
		case info.IsDir():
			item.Kind, item.Navigable = "directory", true
		case info.Mode().IsRegular():
			item.Kind = "file"
		default:
			item.Kind = "other"
		}
		result.Entries = append(result.Entries, item)
	}
	return result, nil
}

func remoteStat(ctx context.Context, client remoteClient, name string) (os.FileInfo, error) {
	type result struct {
		info os.FileInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := client.Stat(name)
		done <- result{info: info, err: err}
	}()
	select {
	case value := <-done:
		return value.info, value.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func remoteGetwd(ctx context.Context, client remoteClient) (string, error) {
	type result struct {
		path string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		name, err := client.Getwd()
		done <- result{path: name, err: err}
	}()
	select {
	case value := <-done:
		return value.path, value.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func remoteReadError(ctx context.Context, prefix string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("превышено время ожидания ответа SFTP-сервера")
	}
	if ctx.Err() != nil {
		return errors.New("чтение SFTP-каталога отменено")
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s: %w", prefix, fs.ErrPermission)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
