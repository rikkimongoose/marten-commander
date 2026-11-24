package sftp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type AttemptState string

const (
	StateDisconnected AttemptState = "disconnected"
	StateConnecting   AttemptState = "connecting"
	StateHostKey      AttemptState = "awaiting_host_key"
	StateConnected    AttemptState = "connected"
	StateError        AttemptState = "error"
	StateCancelled    AttemptState = "cancelled"
)

type ConnectRequest struct {
	ProfileID string `json:"profileId"`
	Secret    string `json:"secret"`
}

type HostKeyChallenge struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   string `json:"expiresAt"`
}

type AttemptStatus struct {
	AttemptID    string            `json:"attemptId"`
	ProfileID    string            `json:"profileId"`
	State        AttemptState      `json:"state"`
	ConnectionID string            `json:"connectionId,omitempty"`
	Challenge    *HostKeyChallenge `json:"challenge,omitempty"`
	Error        string            `json:"error,omitempty"`
}

type attempt struct {
	mu          sync.Mutex
	status      AttemptStatus
	cancel      context.CancelFunc
	conn        net.Conn
	decision    chan bool
	challengeID string
	finishedAt  time.Time
}

type session struct {
	profileID   string
	ssh         *ssh.Client
	sftp        *pkgsftp.Client
	remote      remoteClient
	initialPath string
	timeout     time.Duration
}

type Manager struct {
	profiles       *ProfileStore
	knownHostsPath string
	agentSocket    func() string
	now            func() time.Time

	mu         sync.Mutex
	attempts   map[string]*attempt
	sessions   map[string]*session
	hostKeysMu sync.Mutex
}

func NewManager(profiles *ProfileStore, knownHostsPath string) *Manager {
	return &Manager{
		profiles: profiles, knownHostsPath: knownHostsPath, agentSocket: func() string { return os.Getenv("SSH_AUTH_SOCK") },
		now: time.Now, attempts: make(map[string]*attempt), sessions: make(map[string]*session),
	}
}

func (m *Manager) Connect(request ConnectRequest) (AttemptStatus, error) {
	profile, err := m.profiles.Get(request.ProfileID)
	if err != nil {
		return AttemptStatus{}, err
	}
	if err := ValidateProfile(profile); err != nil {
		return AttemptStatus{}, err
	}
	if profile.AuthMethod == AuthPassword && request.Secret == "" {
		return AttemptStatus{}, errors.New("введите пароль для подключения")
	}
	if profile.AuthMethod == AuthAgent && request.Secret != "" {
		return AttemptStatus{}, errors.New("SSH agent не принимает пароль или passphrase")
	}
	id, err := NewProfileID()
	if err != nil {
		return AttemptStatus{}, err
	}
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	item := &attempt{status: AttemptStatus{AttemptID: id, ProfileID: profile.ID, State: StateConnecting}, cancel: cancel}
	m.mu.Lock()
	m.pruneAttemptsLocked()
	for _, existing := range m.attempts {
		status := existing.snapshot()
		if status.ProfileID == profile.ID && (status.State == StateConnecting || status.State == StateHostKey) {
			m.mu.Unlock()
			cancel()
			return AttemptStatus{}, errors.New("подключение этого профиля уже выполняется")
		}
	}
	for _, current := range m.sessions {
		if current.profileID == profile.ID {
			m.mu.Unlock()
			cancel()
			return AttemptStatus{}, errors.New("этот SFTP-профиль уже подключён")
		}
	}
	m.attempts[id] = item
	m.mu.Unlock()
	go m.run(ctx, item, profile, request.Secret)
	request.Secret = ""
	return item.snapshot(), nil
}

func (m *Manager) GetAttempt(id string) (AttemptStatus, error) {
	m.mu.Lock()
	item := m.attempts[id]
	m.mu.Unlock()
	if item == nil {
		return AttemptStatus{}, errors.New("попытка SFTP-подключения не найдена")
	}
	return item.snapshot(), nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	item := m.attempts[id]
	m.mu.Unlock()
	if item == nil {
		return errors.New("попытка SFTP-подключения не найдена")
	}
	item.mu.Lock()
	state := item.status.State
	if state != StateConnecting && state != StateHostKey {
		item.mu.Unlock()
		return nil
	}
	item.status.State = StateCancelled
	item.status.Challenge = nil
	item.status.Error = ""
	item.finishedAt = m.now()
	cancel, conn := item.cancel, item.conn
	item.mu.Unlock()
	cancel()
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}

func (m *Manager) ResolveHostKey(attemptID, challengeID string, accept bool) error {
	m.mu.Lock()
	item := m.attempts[attemptID]
	m.mu.Unlock()
	if item == nil {
		return errors.New("попытка SFTP-подключения не найдена")
	}
	item.mu.Lock()
	if item.status.State != StateHostKey || item.status.Challenge == nil ||
		subtle.ConstantTimeCompare([]byte(item.challengeID), []byte(challengeID)) != 1 {
		item.mu.Unlock()
		return errors.New("подтверждение ключа сервера устарело или не относится к этой попытке")
	}
	decision := item.decision
	item.status.Challenge = nil
	item.challengeID = ""
	item.mu.Unlock()
	select {
	case decision <- accept:
		return nil
	default:
		return errors.New("решение по ключу сервера уже принято")
	}
}

func (m *Manager) Disconnect(connectionID string) error {
	m.mu.Lock()
	current := m.sessions[connectionID]
	delete(m.sessions, connectionID)
	for _, item := range m.attempts {
		item.mu.Lock()
		if item.status.ConnectionID == connectionID {
			item.status.State = StateDisconnected
			item.status.ConnectionID = ""
			item.finishedAt = m.now()
		}
		item.mu.Unlock()
	}
	m.mu.Unlock()
	if current == nil {
		return errors.New("SFTP-соединение не найдено")
	}
	return closeSession(current)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	attempts := make([]*attempt, 0, len(m.attempts))
	for _, item := range m.attempts {
		attempts = append(attempts, item)
	}
	sessions := m.sessions
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	for _, item := range attempts {
		_ = m.Cancel(item.snapshot().AttemptID)
	}
	var result error
	for _, current := range sessions {
		result = errors.Join(result, closeSession(current))
	}
	return result
}

func (m *Manager) run(ctx context.Context, item *attempt, profile ConnectionProfile, secret string) {
	defer func() { secret = "" }()
	host, err := NetworkHost(profile.Host)
	if err != nil {
		m.fail(item, err)
		return
	}
	address := net.JoinHostPort(host, strconv.Itoa(profile.Port))
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		m.fail(item, connectionError(ctx, "не удалось установить TCP-соединение", err))
		return
	}
	item.mu.Lock()
	if item.status.State == StateCancelled {
		item.mu.Unlock()
		_ = conn.Close()
		return
	}
	item.conn = conn
	item.mu.Unlock()
	defer func() {
		item.mu.Lock()
		item.conn = nil
		item.mu.Unlock()
	}()
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		_ = conn.SetDeadline(deadline)
	}
	stopClose := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopClose:
		}
	}()
	defer close(stopClose)

	auth, authCleanup := m.authMethod(ctx, profile, &secret)
	defer authCleanup()
	config := &ssh.ClientConfig{
		User: profile.Username, Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: m.hostKeyCallback(ctx, item, profile, address),
		Timeout:         time.Until(deadline),
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		m.fail(item, sshError(ctx, err))
		return
	}
	sshClient := ssh.NewClient(clientConn, channels, requests)
	sftpClient, err := pkgsftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		m.fail(item, errors.New("сервер не поддерживает SFTP subsystem"))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	connectionID, err := NewProfileID()
	if err != nil {
		_ = sftpClient.Close()
		_ = sshClient.Close()
		m.fail(item, err)
		return
	}
	readTimeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if readTimeout <= 0 {
		readTimeout = 15 * time.Second
	}
	current := &session{profileID: profile.ID, ssh: sshClient, sftp: sftpClient, remote: packageRemoteClient{sftpClient},
		initialPath: profile.RemotePath, timeout: readTimeout}
	m.mu.Lock()
	m.sessions[connectionID] = current
	m.mu.Unlock()
	item.mu.Lock()
	if item.status.State == StateCancelled {
		item.mu.Unlock()
		m.mu.Lock()
		delete(m.sessions, connectionID)
		m.mu.Unlock()
		_ = closeSession(current)
		return
	}
	item.status.State = StateConnected
	item.status.ConnectionID = connectionID
	item.status.Challenge = nil
	item.status.Error = ""
	item.finishedAt = m.now()
	item.mu.Unlock()
}

func (m *Manager) authMethod(ctx context.Context, profile ConnectionProfile, secret *string) (ssh.AuthMethod, func()) {
	var mu sync.Mutex
	var socket net.Conn
	cleanup := func() {
		mu.Lock()
		defer mu.Unlock()
		if socket != nil {
			_ = socket.Close()
			socket = nil
		}
	}
	switch profile.AuthMethod {
	case AuthPassword:
		return ssh.PasswordCallback(func() (string, error) { return *secret, nil }), cleanup
	case AuthKey:
		return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			data, err := readPrivateKey(profile.PrivateKeyPath)
			if err != nil {
				return nil, err
			}
			var signer ssh.Signer
			if *secret == "" {
				signer, err = ssh.ParsePrivateKey(data)
			} else {
				signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(*secret))
			}
			if err != nil {
				var missing *ssh.PassphraseMissingError
				if errors.As(err, &missing) {
					return nil, errors.New("приватный ключ зашифрован; введите passphrase")
				}
				return nil, errors.New("не удалось прочитать приватный ключ или passphrase неверна")
			}
			return []ssh.Signer{signer}, nil
		}), cleanup
	default:
		return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			path := m.agentSocket()
			if path == "" {
				return nil, errors.New("SSH agent недоступен: SSH_AUTH_SOCK не задан")
			}
			mu.Lock()
			defer mu.Unlock()
			if socket == nil {
				conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", path)
				if err != nil {
					return nil, errors.New("не удалось подключиться к SSH agent")
				}
				socket = conn
			}
			signers, err := agent.NewClient(socket).Signers()
			if err != nil {
				return nil, errors.New("SSH agent не вернул ключи")
			}
			if len(signers) == 0 {
				return nil, errors.New("SSH agent не содержит ключей")
			}
			return signers, nil
		}), cleanup
	}
}

func readPrivateKey(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("не удалось открыть приватный ключ")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errors.New("приватный ключ отсутствует, имеет неверный тип или слишком велик")
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, errors.New("не удалось прочитать приватный ключ")
	}
	return data, nil
}

func (m *Manager) hostKeyCallback(ctx context.Context, item *attempt, profile ConnectionProfile, address string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		known, mismatch, err := m.checkHostKey(hostname, remote, key)
		if err != nil {
			return err
		}
		if known {
			return nil
		}
		if mismatch {
			return errors.New("ключ SSH-сервера изменился; подключение отклонено")
		}
		challengeID, err := NewProfileID()
		if err != nil {
			return err
		}
		decision := make(chan bool, 1)
		expires := m.now().Add(2 * time.Minute)
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
			expires = deadline
		}
		item.mu.Lock()
		if item.status.State == StateCancelled {
			item.mu.Unlock()
			return context.Canceled
		}
		item.status.State = StateHostKey
		item.status.Challenge = &HostKeyChallenge{ID: challengeID, Host: profile.Host, Port: profile.Port,
			Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key), ExpiresAt: expires.UTC().Format(time.RFC3339)}
		item.challengeID = challengeID
		item.decision = decision
		item.mu.Unlock()
		timer := time.NewTimer(time.Until(expires))
		defer timer.Stop()
		select {
		case accept := <-decision:
			if !accept {
				return errors.New("ключ нового SSH-сервера отклонён пользователем")
			}
			return m.rememberHostKey(address, hostname, remote, key)
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("время подтверждения ключа SSH-сервера истекло")
		}
	}
}

func (m *Manager) checkHostKey(hostname string, remote net.Addr, key ssh.PublicKey) (known, mismatch bool, err error) {
	m.hostKeysMu.Lock()
	defer m.hostKeysMu.Unlock()
	callback, err := knownhosts.New(m.knownHostsPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("не удалось прочитать known_hosts: %w", err)
	}
	err = callback(hostname, remote, key)
	if err == nil {
		return true, false, nil
	}
	var keyError *knownhosts.KeyError
	if errors.As(err, &keyError) {
		return false, len(keyError.Want) > 0, nil
	}
	return false, false, errors.New("не удалось проверить ключ SSH-сервера")
}

func (m *Manager) rememberHostKey(address, hostname string, remote net.Addr, key ssh.PublicKey) error {
	m.hostKeysMu.Lock()
	defer m.hostKeysMu.Unlock()
	if callback, err := knownhosts.New(m.knownHostsPath); err == nil {
		if err := callback(hostname, remote, key); err == nil {
			return nil
		} else {
			var keyError *knownhosts.KeyError
			if !errors.As(err, &keyError) || len(keyError.Want) > 0 {
				return errors.New("known_hosts изменился; ключ сервера не сохранён")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("не удалось перечитать known_hosts")
	}
	directory := filepath.Dir(m.knownHostsPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("не удалось создать каталог ~/.ssh")
	}
	file, err := os.OpenFile(m.knownHostsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("не удалось открыть known_hosts для записи")
	}
	line := knownhosts.Line([]string{address}, key) + "\n"
	if _, err := file.WriteString(line); err != nil {
		_ = file.Close()
		return errors.New("не удалось сохранить ключ сервера в known_hosts")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("не удалось синхронизировать known_hosts")
	}
	if err := file.Close(); err != nil {
		return errors.New("не удалось закрыть known_hosts")
	}
	return os.Chmod(m.knownHostsPath, 0o600)
}

func (m *Manager) fail(item *attempt, err error) {
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.status.State == StateCancelled {
		return
	}
	item.status.State = StateError
	item.status.Challenge = nil
	item.status.Error = err.Error()
	item.finishedAt = m.now()
}

func (a *attempt) snapshot() AttemptStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := a.status
	if a.status.Challenge != nil {
		challenge := *a.status.Challenge
		result.Challenge = &challenge
	}
	return result
}

func (m *Manager) pruneAttemptsLocked() {
	cutoff := m.now().Add(-10 * time.Minute)
	for id, item := range m.attempts {
		item.mu.Lock()
		finished := item.finishedAt
		item.mu.Unlock()
		if !finished.IsZero() && finished.Before(cutoff) {
			delete(m.attempts, id)
		}
	}
}

func closeSession(current *session) error {
	var result error
	if current.sftp != nil {
		result = errors.Join(result, current.sftp.Close())
	}
	if current.ssh != nil {
		result = errors.Join(result, current.ssh.Close())
	}
	return result
}

func connectionError(ctx context.Context, prefix string, err error) error {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("превышено время ожидания SFTP-подключения")
		}
		return errors.New("SFTP-подключение отменено")
	}
	return fmt.Errorf("%s: %s", prefix, safeNetworkError(err))
}

func sshError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return connectionError(ctx, "SSH handshake не выполнен", err)
	}
	message := err.Error()
	for _, safe := range []string{"ключ SSH-сервера изменился", "ключ нового SSH-сервера отклонён", "время подтверждения ключа", "known_hosts", "SSH agent", "приватный ключ", "passphrase"} {
		if strings.Contains(message, safe) {
			return errors.New(message)
		}
	}
	return errors.New("SSH-аутентификация не выполнена; проверьте учётные данные и настройки сервера")
}

func safeNetworkError(err error) string {
	var networkError *net.OpError
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "превышено время ожидания"
		}
		return "сервер недоступен"
	}
	return "сервер недоступен"
}
