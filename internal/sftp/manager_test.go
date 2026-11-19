package sftp

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type testSSHServer struct {
	listener  net.Listener
	config    *ssh.ServerConfig
	authCalls atomic.Int32
	mu        sync.Mutex
	clients   map[net.Conn]struct{}
	closed    chan struct{}
}

func newTestSSHServer(t *testing.T, hostSigner ssh.Signer, acceptedKey ssh.PublicKey) *testSSHServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &testSSHServer{listener: listener, clients: make(map[net.Conn]struct{}), closed: make(chan struct{})}
	server.config = &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			server.authCalls.Add(1)
			if metadata.User() == "test-user" && string(password) == "test-password" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
		PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			server.authCalls.Add(1)
			if metadata.User() == "test-user" && acceptedKey != nil && string(key.Marshal()) == string(acceptedKey.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	server.config.AddHostKey(hostSigner)
	go server.serve()
	t.Cleanup(server.close)
	return server
}

func (s *testSSHServer) port() int {
	_, value, _ := net.SplitHostPort(s.listener.Addr().String())
	port, _ := strconv.Atoi(value)
	return port
}

func (s *testSSHServer) serve() {
	defer close(s.closed)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.clients[conn] = struct{}{}
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *testSSHServer) handle(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
	}()
	_, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	for channel := range channels {
		if channel.ChannelType() != "session" {
			_ = channel.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		stream, requests, err := channel.Accept()
		if err != nil {
			continue
		}
		go func() {
			for request := range requests {
				var subsystem struct{ Name string }
				ok := request.Type == "subsystem" && ssh.Unmarshal(request.Payload, &subsystem) == nil && subsystem.Name == "sftp"
				_ = request.Reply(ok, nil)
				if ok {
					server := pkgsftp.NewRequestServer(stream, pkgsftp.InMemHandler())
					_ = server.Serve()
					_ = server.Close()
					return
				}
			}
			_ = stream.Close()
		}()
	}
}

func (s *testSSHServer) close() {
	_ = s.listener.Close()
	s.mu.Lock()
	for conn := range s.clients {
		_ = conn.Close()
	}
	s.mu.Unlock()
	select {
	case <-s.closed:
	case <-time.After(time.Second):
	}
}

func signer(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return private, result
}

func managerProfile(t *testing.T, server *testSSHServer, method AuthMethod, keyPath string) (*Manager, ConnectionProfile, string) {
	t.Helper()
	directory := t.TempDir()
	store := NewProfileStore(filepath.Join(directory, "config"))
	profile := validProfile("test")
	profile.Host = "127.0.0.1"
	profile.Port = server.port()
	profile.Username = "test-user"
	profile.AuthMethod = method
	profile.PrivateKeyPath = keyPath
	profile.RemotePath = ""
	profile.TimeoutSeconds = 5
	saved, err := store.Save(profile)
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(directory, ".ssh", "known_hosts")
	return NewManager(store, knownHosts), saved, knownHosts
}

func waitForState(t *testing.T, manager *Manager, id string, states ...AttemptState) AttemptStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.GetAttempt(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range states {
			if status.State == state {
				return status
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := manager.GetAttempt(id)
	t.Fatalf("attempt stayed in %q, wanted %v: %s", status.State, states, status.Error)
	return AttemptStatus{}
}

func trustChallenge(t *testing.T, manager *Manager, status AttemptStatus) AttemptStatus {
	t.Helper()
	waiting := waitForState(t, manager, status.AttemptID, StateHostKey)
	if waiting.Challenge == nil || !strings.HasPrefix(waiting.Challenge.Fingerprint, "SHA256:") {
		t.Fatalf("invalid host-key challenge: %#v", waiting.Challenge)
	}
	if err := manager.ResolveHostKey(waiting.AttemptID, waiting.Challenge.ID, true); err != nil {
		t.Fatal(err)
	}
	return waitForState(t, manager, waiting.AttemptID, StateConnected, StateError)
}

func TestPasswordHostKeyTrustKnownReconnectAndWrongPassword(t *testing.T) {
	_, host := signer(t)
	_, user := signer(t)
	server := newTestSSHServer(t, host, user.PublicKey())
	manager, profile, knownHosts := managerProfile(t, server, AuthPassword, "")
	t.Cleanup(func() { _ = manager.Close() })

	started, err := manager.Connect(ConnectRequest{ProfileID: profile.ID, Secret: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitForState(t, manager, started.AttemptID, StateHostKey)
	if calls := server.authCalls.Load(); calls != 0 {
		t.Fatalf("credentials were sent before host-key acceptance: %d auth calls", calls)
	}
	if err := manager.ResolveHostKey(started.AttemptID, waiting.Challenge.ID, true); err != nil {
		t.Fatal(err)
	}
	connected := waitForState(t, manager, started.AttemptID, StateConnected)
	if connected.ConnectionID == "" || connected.ConnectionID == profile.ID {
		t.Fatalf("connection id is not independent: %#v", connected)
	}
	if err := manager.Disconnect(connected.ConnectionID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(knownHosts)
	if err != nil || !strings.Contains(string(data), "[127.0.0.1]:") {
		t.Fatalf("non-standard port not stored in known_hosts: %q, %v", data, err)
	}
	address := net.JoinHostPort(profile.Host, strconv.Itoa(profile.Port))
	hashed := knownhosts.Line([]string{knownhosts.HashHostname(knownhosts.Normalize(address))}, host.PublicKey()) + "\n"
	if err := os.WriteFile(knownHosts, []byte(hashed), 0o600); err != nil {
		t.Fatal(err)
	}

	reconnect, err := manager.Connect(ConnectRequest{ProfileID: profile.ID, Secret: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	known := waitForState(t, manager, reconnect.AttemptID, StateConnected, StateError, StateHostKey)
	if known.State != StateConnected {
		t.Fatalf("known host prompted or failed: %#v", known)
	}
	_ = manager.Disconnect(known.ConnectionID)

	wrongSecret := "do-not-leak-this-password"
	wrong, err := manager.Connect(ConnectRequest{ProfileID: profile.ID, Secret: wrongSecret})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForState(t, manager, wrong.AttemptID, StateError)
	if strings.Contains(failed.Error, wrongSecret) {
		t.Fatalf("secret leaked into error: %q", failed.Error)
	}
}

func TestUnknownHostRejectAndChangedHostKeyNeverPrompt(t *testing.T) {
	_, host := signer(t)
	_, user := signer(t)
	server := newTestSSHServer(t, host, user.PublicKey())
	manager, profile, knownHosts := managerProfile(t, server, AuthPassword, "")
	t.Cleanup(func() { _ = manager.Close() })
	started, _ := manager.Connect(ConnectRequest{ProfileID: profile.ID, Secret: "test-password"})
	waiting := waitForState(t, manager, started.AttemptID, StateHostKey)
	if err := manager.ResolveHostKey(started.AttemptID, waiting.Challenge.ID+"stale", true); err == nil {
		t.Fatal("stale challenge was accepted")
	}
	if err := manager.ResolveHostKey(started.AttemptID, waiting.Challenge.ID, false); err != nil {
		t.Fatal(err)
	}
	if state := waitForState(t, manager, started.AttemptID, StateError); !strings.Contains(state.Error, "отклонён") {
		t.Fatalf("unexpected rejection error: %q", state.Error)
	}

	_, otherHost := signer(t)
	if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort(profile.Host, strconv.Itoa(profile.Port))
	if err := os.WriteFile(knownHosts, []byte(knownLine(address, otherHost.PublicKey())), 0o600); err != nil {
		t.Fatal(err)
	}
	mismatch, _ := manager.Connect(ConnectRequest{ProfileID: profile.ID, Secret: "test-password"})
	failed := waitForState(t, manager, mismatch.AttemptID, StateError, StateHostKey)
	if failed.State == StateHostKey || !strings.Contains(failed.Error, "изменился") {
		t.Fatalf("changed key was not rejected as mismatch: %#v", failed)
	}
	if calls := server.authCalls.Load(); calls != 0 {
		t.Fatalf("credentials were sent to a server with a changed key: %d auth calls", calls)
	}
}

func knownLine(address string, key ssh.PublicKey) string {
	return knownhosts.Line([]string{address}, key) + "\n"
}

func TestPrivateKeysEncryptedAndUnencrypted(t *testing.T) {
	private, user := signer(t)
	_, host := signer(t)
	server := newTestSSHServer(t, host, user.PublicKey())
	for _, test := range []struct {
		name, passphrase string
		encrypted        bool
	}{
		{name: "unencrypted"}, {name: "encrypted", passphrase: "test-passphrase", encrypted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var block *pem.Block
			var err error
			if test.encrypted {
				block, err = ssh.MarshalPrivateKeyWithPassphrase(private, "test", []byte(test.passphrase))
			} else {
				block, err = ssh.MarshalPrivateKey(private, "test")
			}
			if err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(t.TempDir(), "id_ed25519")
			if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
				t.Fatal(err)
			}
			manager, profile, _ := managerProfile(t, server, AuthKey, keyPath)
			defer manager.Close()
			started, err := manager.Connect(ConnectRequest{ProfileID: profile.ID, Secret: test.passphrase})
			if err != nil {
				t.Fatal(err)
			}
			result := trustChallenge(t, manager, started)
			if result.State != StateConnected {
				t.Fatalf("key authentication failed: %s", result.Error)
			}
			_ = manager.Disconnect(result.ConnectionID)
			if test.encrypted {
				wrong := mustConnect(t, manager, profile.ID, "wrong-passphrase")
				failed := waitForState(t, manager, wrong.AttemptID, StateError)
				if !strings.Contains(failed.Error, "passphrase") || strings.Contains(failed.Error, "wrong-passphrase") {
					t.Fatalf("bad passphrase error is unsafe or unclear: %q", failed.Error)
				}
			}
		})
	}
}

func TestAgentAvailableUnavailableAndCancellation(t *testing.T) {
	private, user := signer(t)
	_, host := signer(t)
	server := newTestSSHServer(t, host, user.PublicKey())
	manager, profile, _ := managerProfile(t, server, AuthAgent, "")
	manager.agentSocket = func() string { return filepath.Join(t.TempDir(), "missing-agent") }
	missing, _ := manager.Connect(ConnectRequest{ProfileID: profile.ID})
	failed := trustChallenge(t, manager, missing)
	if failed.State != StateError || !strings.Contains(failed.Error, "SSH agent") {
		t.Fatalf("missing agent error is not actionable: %#v", failed)
	}
	_ = manager.Close()

	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, conn); _ = conn.Close() }()
		}
	}()
	manager, profile, _ = managerProfile(t, server, AuthAgent, "")
	manager.agentSocket = func() string { return socketPath }
	defer manager.Close()
	connected := trustChallenge(t, manager, mustConnect(t, manager, profile.ID, ""))
	if connected.State != StateConnected {
		t.Fatalf("agent authentication failed: %s", connected.Error)
	}
	_ = manager.Disconnect(connected.ConnectionID)

	stall, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stall.Close()
	go func() {
		conn, err := stall.Accept()
		if err == nil {
			defer conn.Close()
			time.Sleep(3 * time.Second)
		}
	}()
	stallProfile := profile
	stallProfile.ID = ""
	stallProfile.Port = stall.Addr().(*net.TCPAddr).Port
	stallProfile.AuthMethod = AuthPassword
	stallProfile.TimeoutSeconds = 2
	stallProfile, err = manager.profiles.Save(stallProfile)
	if err != nil {
		t.Fatal(err)
	}
	attempt := mustConnect(t, manager, stallProfile.ID, "test-password")
	if err := manager.Cancel(attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if state := waitForState(t, manager, attempt.AttemptID, StateCancelled); state.State != StateCancelled {
		t.Fatalf("cancel failed: %#v", state)
	}

	timeoutListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer timeoutListener.Close()
	go func() {
		conn, err := timeoutListener.Accept()
		if err == nil {
			defer conn.Close()
			time.Sleep(2 * time.Second)
		}
	}()
	timeoutProfile := stallProfile
	timeoutProfile.ID = ""
	timeoutProfile.Port = timeoutListener.Addr().(*net.TCPAddr).Port
	timeoutProfile.TimeoutSeconds = 1
	timeoutProfile, err = manager.profiles.Save(timeoutProfile)
	if err != nil {
		t.Fatal(err)
	}
	timedOut := mustConnect(t, manager, timeoutProfile.ID, "test-password")
	if state := waitForState(t, manager, timedOut.AttemptID, StateError); !strings.Contains(state.Error, "время ожидания") {
		t.Fatalf("timeout is not reported clearly: %#v", state)
	}
}

func mustConnect(t *testing.T, manager *Manager, profileID, secret string) AttemptStatus {
	t.Helper()
	status, err := manager.Connect(ConnectRequest{ProfileID: profileID, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return status
}
