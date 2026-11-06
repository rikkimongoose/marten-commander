package desktop

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/rikkimongoose/marten-commander/internal/volumes"
)

type VolumeSpace struct {
	Total     string `json:"total"`
	Free      string `json:"free"`
	Available string `json:"available"`
	Used      string `json:"used"`
}

type Volume struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	RootLocation string       `json:"rootLocation"`
	DisplayPath  string       `json:"displayPath"`
	Space        *VolumeSpace `json:"space"`
	Warning      string       `json:"warning"`
}

type HomeDirectory struct {
	Location    string `json:"location"`
	DisplayPath string `json:"displayPath"`
}

type volumeProvider interface {
	ListVolumes(context.Context) ([]volumes.Volume, error)
	GetVolumeInfo(context.Context, string) (volumes.Volume, error)
	GetHomeDirectory(context.Context) (string, error)
}

type VolumeService struct{ provider volumeProvider }

func NewVolumeService(provider volumeProvider) *VolumeService { return &VolumeService{provider} }

func volumeDTO(value volumes.Volume) Volume {
	result := Volume{ID: value.ID, Name: display(value.Name), RootLocation: encode(value.Root), DisplayPath: display(value.Root), Warning: display(value.Warning)}
	if value.Space != nil {
		s := value.Space
		result.Space = &VolumeSpace{strconv.FormatUint(s.Total, 10), strconv.FormatUint(s.Free, 10), strconv.FormatUint(s.Available, 10), strconv.FormatUint(s.Used, 10)}
	}
	return result
}

func (s *VolumeService) ListVolumes(ctx context.Context) ([]Volume, error) {
	items, err := s.provider.ListVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("Не удалось получить список томов: %s", display(err.Error()))
	}
	result := make([]Volume, 0, len(items))
	for _, item := range items {
		result = append(result, volumeDTO(item))
	}
	return result, nil
}

// Accepts the same native-path/opaque-location choice as ReadDirectory.
func (s *VolumeService) GetVolumeInfo(ctx context.Context, request DirectoryRequest) (Volume, error) {
	path := request.Path
	if request.Location != "" {
		if path != "" {
			return Volume{}, fmt.Errorf("укажите путь или идентификатор, но не оба")
		}
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(request.Location)
		if err != nil || len(decoded) == 0 || encode(string(decoded)) != request.Location {
			return Volume{}, fmt.Errorf("некорректный идентификатор каталога")
		}
		path = string(decoded)
	}
	value, err := s.provider.GetVolumeInfo(ctx, path)
	if err != nil {
		return Volume{}, fmt.Errorf("Не удалось определить том: %s", display(err.Error()))
	}
	return volumeDTO(value), nil
}

func (s *VolumeService) GetHomeDirectory(ctx context.Context) (HomeDirectory, error) {
	path, err := s.provider.GetHomeDirectory(ctx)
	if err != nil {
		return HomeDirectory{}, fmt.Errorf("Home недоступен: %s", display(err.Error()))
	}
	return HomeDirectory{Location: encode(path), DisplayPath: display(path)}, nil
}
