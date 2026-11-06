// Package volumes reads local OS mount information. It never mounts devices and
// owns no panel state. Native paths stay byte-exact until the desktop adapter.
package volumes

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type Space struct {
	Total, Free, Available, Used uint64
}

type Volume struct {
	ID, Name, Root string
	Space          *Space
	Warning        string
}

// The small OS boundary permits deterministic tests without real mounts.
type backend interface {
	list(context.Context) ([]Volume, error)
	locate(context.Context, string) (Volume, error)
	space(string) (Space, error)
}

type Service struct {
	backend backend
	home    func() (string, error)
}

func NewService() *Service { return &Service{backend: systemBackend{}, home: os.UserHomeDir} }

func (s *Service) ListVolumes(ctx context.Context) ([]Volume, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items, err := s.backend.list(ctx)
	if err != nil {
		return nil, err
	}
	// Enumeration deliberately avoids probing every network/removable device.
	// Space is read on demand for the current directory by GetVolumeInfo.
	if items == nil {
		items = []Volume{}
	}
	return items, ctx.Err()
}

func (s *Service) GetVolumeInfo(ctx context.Context, path string) (Volume, error) {
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return Volume{}, fmt.Errorf("путь должен быть абсолютным")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Volume{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Volume{}, err
	}
	volume, err := s.backend.locate(ctx, resolved)
	if err != nil {
		return Volume{}, err
	}
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	spacePath := resolved
	// Locate the file itself first: a Linux file bind mount can live on a
	// different filesystem from its parent. Win32 capacity accepts directories.
	if !info.IsDir() {
		spacePath = volume.Root
	}
	space, err := s.backend.space(spacePath)
	if err != nil {
		volume.Warning = "Не удалось определить объём: " + err.Error()
	} else {
		volume.Space = &space
	}
	return volume, ctx.Err()
}

func (s *Service) GetHomeDirectory(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := s.home()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("не удалось определить абсолютный путь Home")
	}
	return path, nil
}

func spaceFromCounts(total, free, available, blockSize uint64) (Space, error) {
	if blockSize == 0 || total > math.MaxUint64/blockSize || free > total || available > free {
		return Space{}, fmt.Errorf("некорректные счётчики файловой системы")
	}
	return Space{Total: total * blockSize, Free: free * blockSize,
		Available: available * blockSize, Used: (total - free) * blockSize}, nil
}
