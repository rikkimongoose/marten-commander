// Package app implements GUI-independent application use cases.
package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

type DirectoryEntry struct {
	core.FileEntry
	Navigable   bool
	TargetError error // A broken/inaccessible link does not invalidate the listing.
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
