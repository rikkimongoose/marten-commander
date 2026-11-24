package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/viewer"
)

// remoteClient is the read-only part of pkg/sftp.Client used by directory
// browsing. Keeping it narrow makes navigation testable without an SSH server.
type remoteClient interface {
	Getwd() (string, error)
	ReadDirContext(context.Context, string) ([]os.FileInfo, error)
	Stat(string) (os.FileInfo, error)
	Lstat(string) (os.FileInfo, error)
	Open(string) (remoteFile, error)
}

type remoteFile interface {
	io.ReadCloser
	ReadAt([]byte, int64) (int, error)
}

type packageRemoteClient struct{ client *pkgsftp.Client }

func (c packageRemoteClient) Getwd() (string, error) { return c.client.Getwd() }
func (c packageRemoteClient) ReadDirContext(ctx context.Context, name string) ([]os.FileInfo, error) {
	return c.client.ReadDirContext(ctx, name)
}
func (c packageRemoteClient) Stat(name string) (os.FileInfo, error)  { return c.client.Stat(name) }
func (c packageRemoteClient) Lstat(name string) (os.FileInfo, error) { return c.client.Lstat(name) }
func (c packageRemoteClient) Open(name string) (remoteFile, error)   { return c.client.Open(name) }

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

func (m *Manager) Lstat(ctx context.Context, connectionID, name string) (core.FileEntry, error) {
	current, err := m.session(connectionID)
	if err != nil {
		return core.FileEntry{}, err
	}
	if !path.IsAbs(name) || strings.ContainsRune(name, 0) {
		return core.FileEntry{}, fs.ErrInvalid
	}
	info, err := remoteLstat(ctx, current.remote, path.Clean(name))
	if err != nil {
		return core.FileEntry{}, remoteReadError(ctx, "не удалось прочитать сведения о SFTP-файле", err)
	}
	return core.FileEntry{Path: path.Clean(name), Name: info.Name(), Kind: core.KindFromMode(info.Mode()),
		Size: info.Size(), ModifiedAt: info.ModTime(), Mode: info.Mode(), Hidden: strings.HasPrefix(info.Name(), ".")}, nil
}

func (m *Manager) Open(ctx context.Context, connectionID, name string) (io.ReadCloser, error) {
	current, err := m.session(connectionID)
	if err != nil {
		return nil, err
	}
	if !path.IsAbs(name) || strings.ContainsRune(name, 0) {
		return nil, fs.ErrInvalid
	}
	file, err := remoteOpen(ctx, current.remote, path.Clean(name))
	if err != nil {
		return nil, remoteReadError(ctx, "не удалось открыть SFTP-файл", err)
	}
	reader := &remoteContextReader{ctx: ctx, file: file}
	reader.stopCancellation = context.AfterFunc(ctx, func() { _ = reader.closeFile() })
	return reader, nil
}

func (m *Manager) OpenViewerFile(ctx context.Context, connectionID, name string) (viewer.File, error) {
	entry, err := m.Lstat(ctx, connectionID, name)
	if err != nil {
		return nil, err
	}
	if entry.Kind != core.KindFile {
		return nil, viewer.ErrNotRegular
	}
	current, err := m.session(connectionID)
	if err != nil {
		return nil, err
	}
	file, err := remoteOpen(ctx, current.remote, entry.Path)
	if err != nil {
		return nil, err
	}
	return &remoteViewerFile{file: file, entry: entry}, nil
}

func (m *Manager) session(connectionID string) (*session, error) {
	m.mu.Lock()
	current := m.sessions[connectionID]
	m.mu.Unlock()
	if current == nil {
		return nil, errors.New("SFTP-соединение закрыто")
	}
	return current, nil
}

func remoteLstat(ctx context.Context, client remoteClient, name string) (os.FileInfo, error) {
	type result struct {
		info os.FileInfo
		err  error
	}
	done := make(chan result, 1)
	go func() { info, err := client.Lstat(name); done <- result{info, err} }()
	select {
	case value := <-done:
		return value.info, value.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func remoteOpen(ctx context.Context, client remoteClient, name string) (remoteFile, error) {
	type result struct {
		file remoteFile
		err  error
	}
	done := make(chan result, 1)
	go func() { file, err := client.Open(name); done <- result{file, err} }()
	select {
	case value := <-done:
		return value.file, value.err
	case <-ctx.Done():
		go func() {
			value := <-done
			if value.file != nil {
				_ = value.file.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

type remoteContextReader struct {
	ctx              context.Context
	file             remoteFile
	stopCancellation func() bool
	closeOnce        sync.Once
	closeErr         error
}

func (r *remoteContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.file.Read(buffer)
	if contextErr := r.ctx.Err(); contextErr != nil {
		return n, contextErr
	}
	return n, err
}
func (r *remoteContextReader) closeFile() error {
	r.closeOnce.Do(func() { r.closeErr = r.file.Close() })
	return r.closeErr
}
func (r *remoteContextReader) Close() error {
	if r.stopCancellation != nil {
		r.stopCancellation()
	}
	return r.closeFile()
}

type remoteViewerFile struct {
	file  remoteFile
	entry core.FileEntry
}

func (f *remoteViewerFile) Metadata(ctx context.Context) (viewer.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return viewer.Metadata{}, err
	}
	return viewer.Metadata{Name: f.entry.Name, Path: f.entry.Path, Size: f.entry.Size,
		ModifiedAt: f.entry.ModifiedAt.UTC().Format(time.RFC3339Nano)}, nil
}
func (f *remoteViewerFile) ReadAt(ctx context.Context, offset int64, length int) ([]byte, error) {
	if offset < 0 {
		return nil, viewer.ErrInvalidOffset
	}
	if length < 0 || length > viewer.MaxReadSize {
		return nil, viewer.ErrInvalidLength
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data := make([]byte, length)
	n, err := f.file.ReadAt(data, offset)
	data = data[:n]
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return data, err
}
func (f *remoteViewerFile) Close() error { return f.file.Close() }
