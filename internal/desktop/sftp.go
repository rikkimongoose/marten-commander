package desktop

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	remotesftp "github.com/rikkimongoose/marten-commander/internal/sftp"
)

// SFTPService is the Wails boundary. Secrets are accepted only by Connect and
// are never returned by any method or placed into a profile.
type SFTPService struct {
	profiles *remotesftp.ProfileStore
	manager  *remotesftp.Manager
	styler   fileStyler
}

func NewSFTPService(profiles *remotesftp.ProfileStore, manager *remotesftp.Manager, stylers ...fileStyler) *SFTPService {
	service := &SFTPService{profiles: profiles, manager: manager}
	if len(stylers) > 0 {
		service.styler = stylers[0]
	}
	return service
}

func (s *SFTPService) ListProfiles(ctx context.Context) ([]remotesftp.ConnectionProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	profiles, err := s.profiles.List()
	if err != nil {
		return nil, fmt.Errorf("Не удалось загрузить SFTP-профили: %w", err)
	}
	return profiles, nil
}

func (s *SFTPService) SaveProfile(ctx context.Context, profile remotesftp.ConnectionProfile) (remotesftp.ConnectionProfile, error) {
	if err := ctx.Err(); err != nil {
		return remotesftp.ConnectionProfile{}, err
	}
	saved, err := s.profiles.Save(profile)
	if err != nil {
		return remotesftp.ConnectionProfile{}, fmt.Errorf("Не удалось сохранить SFTP-профиль: %w", err)
	}
	return saved, nil
}

func (s *SFTPService) DeleteProfile(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.profiles.Delete(id); err != nil {
		return fmt.Errorf("Не удалось удалить SFTP-профиль: %w", err)
	}
	return nil
}

func (s *SFTPService) Connect(ctx context.Context, request remotesftp.ConnectRequest) (remotesftp.AttemptStatus, error) {
	if err := ctx.Err(); err != nil {
		return remotesftp.AttemptStatus{}, err
	}
	status, err := s.manager.Connect(request)
	request.Secret = ""
	if err != nil {
		return remotesftp.AttemptStatus{}, fmt.Errorf("Не удалось начать SFTP-подключение: %w", err)
	}
	return status, nil
}

func (s *SFTPService) GetConnectionStatus(ctx context.Context, attemptID string) (remotesftp.AttemptStatus, error) {
	if err := ctx.Err(); err != nil {
		return remotesftp.AttemptStatus{}, err
	}
	return s.manager.GetAttempt(attemptID)
}

func (s *SFTPService) ResolveHostKey(ctx context.Context, attemptID, challengeID string, accept bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.manager.ResolveHostKey(attemptID, challengeID, accept)
}

func (s *SFTPService) CancelConnection(ctx context.Context, attemptID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.manager.Cancel(attemptID)
}

func (s *SFTPService) Disconnect(ctx context.Context, connectionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.manager.Disconnect(connectionID)
}

type RemoteDirectoryRequest struct {
	ConnectionID string `json:"connectionId"`
	Path         string `json:"path"`
	Location     string `json:"location"`
	BaseLocation string `json:"baseLocation"`
}

// ReadRemoteDirectory browses only an existing Manager session: authentication,
// host-key policy and session ownership remain in the connection flow.
func (s *SFTPService) ReadRemoteDirectory(ctx context.Context, request RemoteDirectoryRequest) (Directory, error) {
	if request.ConnectionID == "" {
		return Directory{}, fmt.Errorf("SFTP-соединение не указано")
	}
	remotePath := request.Path
	if request.Location != "" {
		if remotePath != "" || request.BaseLocation != "" {
			return Directory{}, fmt.Errorf("укажите путь или идентификатор каталога, но не оба")
		}
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(request.Location)
		if err != nil || len(decoded) == 0 {
			return Directory{}, fmt.Errorf("некорректный идентификатор SFTP-каталога")
		}
		remotePath = string(decoded)
	} else if request.BaseLocation != "" {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(request.BaseLocation)
		if err != nil || len(decoded) == 0 || remotePath == "" {
			return Directory{}, fmt.Errorf("некорректный базовый SFTP-каталог")
		}
		if !path.IsAbs(remotePath) {
			remotePath = path.Join(string(decoded), remotePath)
		}
	}
	directory, err := s.manager.ReadDirectory(ctx, request.ConnectionID, remotePath)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrInvalid):
			return Directory{}, fmt.Errorf("укажите абсолютный POSIX-путь")
		case errors.Is(err, fs.ErrPermission):
			return Directory{}, fmt.Errorf("Нет прав на чтение SFTP-каталога %s", remotePath)
		default:
			return Directory{}, fmt.Errorf("Не удалось прочитать SFTP-каталог %s: %v", remotePath, err)
		}
	}
	result := Directory{Source: "remote", ConnectionID: request.ConnectionID, Location: encode(directory.Path),
		ParentLocation: encode(directory.Parent), DisplayPath: directory.Path, Entries: make([]Entry, 0, len(directory.Entries)),
		AncestorLocations: make([]string, 0)}
	for current := directory.Path; current != "/"; {
		current = path.Dir(current)
		result.AncestorLocations = append(result.AncestorLocations, encode(current))
	}
	for _, entry := range directory.Entries {
		item := Entry{ID: encode(entry.Path), Name: entry.Name, Kind: entry.Kind, Hidden: entry.Hidden,
			Navigable: entry.Navigable, Detail: entry.Detail}
		if entry.Kind == "file" {
			item.Size = strconv.FormatInt(entry.Size, 10)
		}
		if !entry.ModifiedAt.IsZero() {
			item.ModifiedAt = entry.ModifiedAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
		if dot := strings.LastIndexByte(entry.Name, '.'); dot > 0 && !entry.Navigable {
			item.Extension = entry.Name[dot+1:]
		}
		applyFileStyle(s.styler, item.Kind, entry.Name, &item)
		result.Entries = append(result.Entries, item)
	}
	return result, nil
}

func (s *SFTPService) ServiceShutdown() error { return s.manager.Close() }
