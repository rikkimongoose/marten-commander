package sftp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const profileFileName = "sftp-profiles.json"

type profileDocument struct {
	Version  int                 `json:"version"`
	Profiles []ConnectionProfile `json:"profiles"`
}

type ProfileStore struct {
	directory string
	mu        sync.Mutex
}

func NewProfileStore(configDirectory string) *ProfileStore {
	return &ProfileStore{directory: configDirectory}
}

func (s *ProfileStore) Path() string { return filepath.Join(s.directory, profileFileName) }

func (s *ProfileStore) List() ([]ConnectionProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *ProfileStore) Save(profile ConnectionProfile) (ConnectionProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if profile.ID == "" {
		id, err := NewProfileID()
		if err != nil {
			return ConnectionProfile{}, err
		}
		profile.ID = id
	}
	if profile.Port == 0 {
		profile.Port = 22
	}
	if profile.TimeoutSeconds == 0 {
		profile.TimeoutSeconds = 15
	}
	if err := ValidateProfile(profile); err != nil {
		return ConnectionProfile{}, err
	}
	profiles, err := s.load()
	if err != nil {
		return ConnectionProfile{}, err
	}
	found := false
	for index := range profiles {
		if profiles[index].ID == profile.ID {
			profiles[index] = profile
			found = true
			break
		}
	}
	if !found {
		profiles = append(profiles, profile)
	}
	if err := s.write(profiles); err != nil {
		return ConnectionProfile{}, err
	}
	return profile, nil
}

func (s *ProfileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles, err := s.load()
	if err != nil {
		return err
	}
	result := make([]ConnectionProfile, 0, len(profiles))
	found := false
	for _, profile := range profiles {
		if profile.ID == id {
			found = true
			continue
		}
		result = append(result, profile)
	}
	if !found {
		return errors.New("профиль SFTP не найден")
	}
	return s.write(result)
}

func (s *ProfileStore) Get(id string) (ConnectionProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles, err := s.load()
	if err != nil {
		return ConnectionProfile{}, err
	}
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return ConnectionProfile{}, errors.New("профиль SFTP не найден")
}

func (s *ProfileStore) load() ([]ConnectionProfile, error) {
	file, err := os.Open(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return []ConnectionProfile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать профили SFTP: %w", err)
	}
	defer file.Close()
	var document profileDocument
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("файл профилей SFTP повреждён: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("файл профилей SFTP повреждён: %w", err)
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("неподдерживаемая версия файла профилей SFTP: %d", document.Version)
	}
	seen := make(map[string]struct{}, len(document.Profiles))
	for _, profile := range document.Profiles {
		if err := ValidateProfile(profile); err != nil {
			return nil, fmt.Errorf("профиль %q некорректен: %w", profile.Name, err)
		}
		if _, exists := seen[profile.ID]; exists {
			return nil, fmt.Errorf("файл профилей SFTP содержит повторяющийся id %q", profile.ID)
		}
		seen[profile.ID] = struct{}{}
	}
	return append([]ConnectionProfile(nil), document.Profiles...), nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("после JSON обнаружены лишние данные")
		}
		return err
	}
	return nil
}

func (s *ProfileStore) write(profiles []ConnectionProfile) error {
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("не удалось создать каталог профилей SFTP: %w", err)
	}
	if err := os.Chmod(s.directory, 0o700); err != nil {
		return fmt.Errorf("не удалось ограничить права каталога профилей SFTP: %w", err)
	}
	sort.SliceStable(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	temporary, err := os.CreateTemp(s.directory, ".sftp-profiles-*.tmp")
	if err != nil {
		return fmt.Errorf("не удалось создать временный файл профилей SFTP: %w", err)
	}
	temporaryName := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("не удалось ограничить права файла профилей SFTP: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(profileDocument{Version: 1, Profiles: profiles}); err != nil {
		return fmt.Errorf("не удалось записать профили SFTP: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("не удалось синхронизировать профили SFTP: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("не удалось закрыть файл профилей SFTP: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path()); err != nil {
		return fmt.Errorf("не удалось атомарно сохранить профили SFTP: %w", err)
	}
	keep = true
	if err := os.Chmod(s.Path(), 0o600); err != nil {
		return fmt.Errorf("не удалось ограничить права файла профилей SFTP: %w", err)
	}
	if directory, err := os.Open(s.directory); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
