package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"reflect"
	"testing"
	"time"
)

type remoteInfo struct {
	name string
	size int64
	mode fs.FileMode
	when time.Time
}

func (i remoteInfo) Name() string       { return i.name }
func (i remoteInfo) Size() int64        { return i.size }
func (i remoteInfo) Mode() fs.FileMode  { return i.mode }
func (i remoteInfo) ModTime() time.Time { return i.when }
func (i remoteInfo) IsDir() bool        { return i.mode.IsDir() }
func (i remoteInfo) Sys() any           { return nil }

type mockRemote struct {
	wd        string
	entries   map[string][]os.FileInfo
	stat      map[string]os.FileInfo
	errors    map[string]error
	requested []string
	block     bool
}

func (m *mockRemote) Getwd() (string, error) { return m.wd, nil }
func (m *mockRemote) ReadDirContext(ctx context.Context, name string) ([]os.FileInfo, error) {
	m.requested = append(m.requested, name)
	if m.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := m.errors[name]; err != nil {
		return nil, err
	}
	return append([]os.FileInfo(nil), m.entries[name]...), nil
}
func (m *mockRemote) Stat(name string) (os.FileInfo, error) {
	value, ok := m.stat[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return value, nil
}
func (m *mockRemote) Lstat(name string) (os.FileInfo, error) { return m.Stat(name) }
func (m *mockRemote) Open(string) (remoteFile, error)        { return nil, errors.ErrUnsupported }

func browserManager(id string, remote remoteClient, initial string, timeout time.Duration) *Manager {
	return &Manager{sessions: map[string]*session{id: {remote: remote, initialPath: initial, timeout: timeout}}, attempts: make(map[string]*attempt)}
}

func TestReadDirectoryMockTransportMetadataPOSIXAndGetwd(t *testing.T) {
	when := time.Date(2026, 11, 24, 12, 30, 0, 0, time.FixedZone("server", 3*60*60))
	remote := &mockRemote{wd: "/home/ёж", entries: map[string][]os.FileInfo{
		"/home/ёж": {
			remoteInfo{name: "пусто", mode: fs.ModeDir | 0755, when: when},
			remoteInfo{name: "данные.txt", size: 1 << 40, mode: 0644, when: when},
			remoteInfo{name: "ссылка", mode: fs.ModeSymlink | 0777, when: when},
		},
	}, stat: map[string]os.FileInfo{"/home/ёж/ссылка": remoteInfo{name: "ссылка", mode: fs.ModeDir | 0755}}, errors: map[string]error{}}
	manager := browserManager("current", remote, "", time.Second)
	directory, err := manager.ReadDirectory(context.Background(), "current", "")
	if err != nil {
		t.Fatal(err)
	}
	if directory.Path != "/home/ёж" || directory.Parent != "/home" || len(directory.Entries) != 3 {
		t.Fatalf("directory = %#v", directory)
	}
	gotKinds := []string{directory.Entries[0].Kind, directory.Entries[1].Kind, directory.Entries[2].Kind}
	if !reflect.DeepEqual(gotKinds, []string{"directory", "file", "symlink"}) || !directory.Entries[2].Navigable {
		t.Fatalf("entries = %#v", directory.Entries)
	}
	if directory.Entries[1].Size != 1<<40 || !directory.Entries[1].ModifiedAt.Equal(when) {
		t.Fatalf("metadata = %#v", directory.Entries[1])
	}
}

func TestReadDirectoryMockTransportInitialEmptyLargeAndErrors(t *testing.T) {
	large := make([]os.FileInfo, 4097)
	for index := range large {
		large[index] = remoteInfo{name: "item-" + string(rune(0x1000+index)), mode: 0644}
	}
	remote := &mockRemote{entries: map[string][]os.FileInfo{"/start": {}, "/large": large},
		stat: map[string]os.FileInfo{}, errors: map[string]error{"/denied": fs.ErrPermission}}
	manager := browserManager("connection", remote, "/start", time.Second)
	empty, err := manager.ReadDirectory(context.Background(), "connection", "")
	if err != nil || empty.Entries == nil || len(empty.Entries) != 0 || remote.requested[0] != "/start" {
		t.Fatalf("empty = %#v, %v, requested=%v", empty, err, remote.requested)
	}
	listed, err := manager.ReadDirectory(context.Background(), "connection", "/large/../large")
	if err != nil || listed.Path != "/large" || len(listed.Entries) != len(large) {
		t.Fatalf("large = %d, path=%q, err=%v", len(listed.Entries), listed.Path, err)
	}
	if _, err := manager.ReadDirectory(context.Background(), "connection", "/denied"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission error = %v", err)
	}
	if _, err := manager.ReadDirectory(context.Background(), "old-connection", "/"); err == nil {
		t.Fatal("closed session was accepted")
	}
}

func TestReadDirectoryMockTransportCancellationAndTimeout(t *testing.T) {
	remote := &mockRemote{block: true, entries: map[string][]os.FileInfo{}, stat: map[string]os.FileInfo{}, errors: map[string]error{}}
	manager := browserManager("connection", remote, "/", 20*time.Millisecond)
	if _, err := manager.ReadDirectory(context.Background(), "connection", "/"); err == nil || err.Error() != "превышено время ожидания ответа SFTP-сервера" {
		t.Fatalf("timeout error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.ReadDirectory(ctx, "connection", "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestReadDirectoryAgainstLocalSFTPStand(t *testing.T) {
	_, host := signer(t)
	_, user := signer(t)
	server := newTestSSHServer(t, host, user.PublicKey())
	manager, profile, _ := managerProfile(t, server, AuthPassword, "")
	t.Cleanup(func() { _ = manager.Close() })
	started, err := manager.Connect(ConnectRequest{ProfileID: profile.ID, Secret: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	connected := trustChallenge(t, manager, started)
	if connected.State != StateConnected {
		t.Fatalf("connection = %#v", connected)
	}
	directory, err := manager.ReadDirectory(context.Background(), connected.ConnectionID, "/")
	if err != nil || directory.Path != "/" || directory.Entries == nil {
		t.Fatalf("root = %#v, %v", directory, err)
	}
	current, err := manager.session(connected.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := current.remote.(packageRemoteClient)
	if !ok {
		t.Fatalf("unexpected remote transport %T", current.remote)
	}
	contents := bytes.Repeat([]byte("SFTP-данные\n"), 1024)
	remoteName := "/файл с пробелами.txt"
	created, err := transport.client.Create(remoteName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := created.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	entry, err := manager.Lstat(context.Background(), connected.ConnectionID, remoteName)
	if err != nil || entry.Kind != "file" || entry.Size != int64(len(contents)) {
		t.Fatalf("lstat = %#v, %v", entry, err)
	}
	reader, err := manager.Open(context.Background(), connected.ConnectionID, remoteName)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(downloaded, contents) {
		t.Fatalf("downloaded %d bytes, read=%v close=%v", len(downloaded), readErr, closeErr)
	}
	viewed, err := manager.OpenViewerFile(context.Background(), connected.ConnectionID, remoteName)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := viewed.ReadAt(context.Background(), 5, 128)
	if err != nil || !bytes.Equal(chunk, contents[5:5+128]) {
		t.Fatalf("viewer range = %q, %v", chunk, err)
	}
	if err := viewed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(connected.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReadDirectory(context.Background(), connected.ConnectionID, "/"); err == nil {
		t.Fatal("disconnected transport remained browseable")
	}
}
