package desktop

import (
	"context"
	"fmt"

	remotesftp "github.com/rikkimongoose/marten-commander/internal/sftp"
)

// SFTPService is the Wails boundary. Secrets are accepted only by Connect and
// are never returned by any method or placed into a profile.
type SFTPService struct {
	profiles *remotesftp.ProfileStore
	manager  *remotesftp.Manager
}

func NewSFTPService(profiles *remotesftp.ProfileStore, manager *remotesftp.Manager) *SFTPService {
	return &SFTPService{profiles: profiles, manager: manager}
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

func (s *SFTPService) ServiceShutdown() error { return s.manager.Close() }
