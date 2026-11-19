package sftp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

type AuthMethod string

const (
	AuthAgent    AuthMethod = "agent"
	AuthKey      AuthMethod = "key"
	AuthPassword AuthMethod = "password"
)

// ConnectionProfile deliberately contains no authentication secret. Passwords
// and key passphrases exist only in a ConnectRequest for one attempt.
type ConnectionProfile struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Host           string     `json:"host"`
	Port           int        `json:"port"`
	Username       string     `json:"username"`
	AuthMethod     AuthMethod `json:"authMethod"`
	PrivateKeyPath string     `json:"privateKeyPath,omitempty"`
	RemotePath     string     `json:"remotePath,omitempty"`
	TimeoutSeconds int        `json:"timeoutSeconds,omitempty"`
}

func NewProfileID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("не удалось создать идентификатор профиля: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func ValidateProfile(profile ConnectionProfile) error {
	if profile.ID == "" || len(profile.ID) > 128 || strings.IndexFunc(profile.ID, invalidIdentifierRune) >= 0 {
		return errors.New("некорректный идентификатор профиля")
	}
	if strings.TrimSpace(profile.Name) == "" || containsControl(profile.Name) {
		return errors.New("укажите название профиля")
	}
	if _, err := NetworkHost(profile.Host); err != nil {
		return err
	}
	if profile.Port < 1 || profile.Port > 65535 {
		return errors.New("порт должен быть в диапазоне 1–65535")
	}
	if strings.TrimSpace(profile.Username) == "" || containsControl(profile.Username) {
		return errors.New("укажите имя пользователя")
	}
	switch profile.AuthMethod {
	case AuthAgent, AuthPassword:
		if profile.PrivateKeyPath != "" {
			return errors.New("путь к приватному ключу допустим только для метода key")
		}
	case AuthKey:
		if strings.TrimSpace(profile.PrivateKeyPath) == "" || strings.IndexByte(profile.PrivateKeyPath, 0) >= 0 {
			return errors.New("укажите путь к приватному SSH-ключу")
		}
	default:
		return errors.New("неизвестный способ аутентификации")
	}
	if profile.RemotePath != "" {
		if containsControl(profile.RemotePath) || !path.IsAbs(profile.RemotePath) {
			return errors.New("начальный каталог должен быть абсолютным POSIX-путём")
		}
	}
	if profile.TimeoutSeconds < 0 || profile.TimeoutSeconds > 300 {
		return errors.New("таймаут должен быть от 1 до 300 секунд")
	}
	return nil
}

func NetworkHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if host == "" || containsControl(host) || strings.ContainsAny(host, "/@") {
		return "", errors.New("укажите корректный hostname или IP-адрес")
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || len(ascii) > 253 || strings.Contains(ascii, ":") {
		return "", errors.New("укажите корректный hostname или IP-адрес")
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("укажите корректный hostname или IP-адрес")
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '-') {
				return "", errors.New("укажите корректный hostname или IP-адрес")
			}
		}
	}
	return ascii, nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) }) >= 0
}

func invalidIdentifierRune(r rune) bool {
	return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
}
