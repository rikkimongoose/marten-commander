// Package app implements GUI-independent application use cases.
package app

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

type DirectoryEntry struct {
	core.FileEntry
	Navigable   bool
	TargetError error // A broken/inaccessible link does not invalidate the listing.
}

// CalculateDirectorySize recursively sums regular files without following
// symbolic links. Any unreadable subtree invalidates the complete result so
// the UI never presents a partial byte count as exact.
func (s *DirectoryService) CalculateDirectorySize(ctx context.Context, path string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	entry, err := s.filesystem.Lstat(ctx, path)
	if err != nil {
		return 0, fmt.Errorf("inspect directory: %w", err)
	}
	if entry.Kind != core.KindDirectory {
		return 0, fmt.Errorf("inspect directory: %w", fs.ErrInvalid)
	}
	return s.calculateDirectorySize(ctx, path)
}

func (s *DirectoryService) calculateDirectorySize(ctx context.Context, path string) (uint64, error) {
	entries, err := s.filesystem.ReadDir(ctx, path)
	if err != nil {
		return 0, fmt.Errorf("read directory %q: %w", path, err)
	}
	var total uint64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var size uint64
		switch entry.Kind {
		case core.KindFile:
			if entry.Size < 0 {
				return 0, fmt.Errorf("invalid size for %q", entry.Path)
			}
			size = uint64(entry.Size)
		case core.KindDirectory:
			size, err = s.calculateDirectorySize(ctx, entry.Path)
			if err != nil {
				return 0, err
			}
		case core.KindSymlink, core.KindOther:
			continue
		}
		if size > math.MaxUint64-total {
			return 0, fmt.Errorf("directory size overflow at %q", entry.Path)
		}
		total += size
	}
	return total, nil
}

type Directory struct {
	Path    string
	Parent  string
	Entries []DirectoryEntry
}

// DirectoryService is stateless: each panel owns its current snapshot in React.
// Path handling here is local-platform navigation, not a universal VFS.
type DirectoryService struct {
	filesystem core.FileSystem
	resolver   core.PathResolver
	initial    string
}

func NewDirectoryService(filesystem core.FileSystem, resolver core.PathResolver, initial string) *DirectoryService {
	return &DirectoryService{filesystem: filesystem, resolver: resolver, initial: initial}
}

func (s *DirectoryService) ReadDirectory(ctx context.Context, path string) (Directory, error) {
	if err := ctx.Err(); err != nil {
		return Directory{}, err
	}
	if path == "" {
		path = s.initial
	}
	resolved, err := s.resolver.ResolvePath(ctx, path)
	if err != nil {
		return Directory{}, fmt.Errorf("resolve directory: %w", err)
	}
	entries, err := s.filesystem.ReadDir(ctx, resolved)
	if err != nil {
		return Directory{}, fmt.Errorf("read directory: %w", err)
	}
	result := Directory{Path: resolved, Parent: filepath.Dir(resolved), Entries: make([]DirectoryEntry, 0, len(entries))}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Directory{}, err
		}
		item := DirectoryEntry{FileEntry: entry, Navigable: entry.Kind == core.KindDirectory}
		if entry.Kind == core.KindSymlink {
			target, err := s.resolver.ResolvePath(ctx, entry.Path)
			if err == nil {
				var info core.FileEntry
				info, err = s.filesystem.Lstat(ctx, target)
				item.Navigable = err == nil && info.Kind == core.KindDirectory
			}
			item.TargetError = err
		}
		result.Entries = append(result.Entries, item)
	}
	if err := ctx.Err(); err != nil {
		return Directory{}, err
	}
	return result, nil
}
