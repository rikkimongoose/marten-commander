package app_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
)

type source struct {
	core.FileSystem
	read    func(context.Context, string) ([]core.FileEntry, error)
	resolve func(context.Context, string) (string, error)
}

func (s source) ReadDir(ctx context.Context, path string) ([]core.FileEntry, error) {
	return s.read(ctx, path)
}
func (s source) ResolvePath(ctx context.Context, path string) (string, error) {
	if s.resolve != nil {
		return s.resolve(ctx, path)
	}
	return path, ctx.Err()
}

func TestInitialDirectoryAndProviderOrder(t *testing.T) {
	initial := t.TempDir()
	provider := source{read: func(_ context.Context, path string) ([]core.FileEntry, error) {
		if path != initial {
			t.Fatalf("path = %q, want initial %q", path, initial)
		}
		return []core.FileEntry{{Name: "z", Kind: core.KindFile}, {Name: ".a", Kind: core.KindFile, Hidden: true}, {Name: "dir", Kind: core.KindDirectory}}, nil
	}}
	service := app.NewDirectoryService(provider, provider, initial)
	directory, err := service.ReadDirectory(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if directory.Path != initial || directory.Parent != filepath.Dir(initial) || len(directory.Entries) != 3 {
		t.Fatalf("unexpected directory: %+v", directory)
	}
	if directory.Entries[0].Name != "z" || directory.Entries[1].Name != ".a" || !directory.Entries[1].Hidden || !directory.Entries[2].Navigable {
		t.Fatalf("provider order/metadata changed: %+v", directory.Entries)
	}
}

func TestErrorsAndCancellation(t *testing.T) {
	for _, cause := range []error{fs.ErrPermission, fs.ErrNotExist, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			provider := source{read: func(context.Context, string) ([]core.FileEntry, error) {
				return []core.FileEntry{{Name: "partial"}}, cause
			}}
			service := app.NewDirectoryService(provider, provider, t.TempDir())
			directory, err := service.ReadDirectory(context.Background(), "")
			if !errors.Is(err, cause) || directory.Entries != nil {
				t.Fatalf("directory = %+v, error = %v; want %v without partial listing", directory, err, cause)
			}
		})
	}
	provider := source{resolve: func(context.Context, string) (string, error) { return "", fs.ErrNotExist }}
	service := app.NewDirectoryService(provider, provider, t.TempDir())
	if _, err := service.ReadDirectory(context.Background(), ""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("resolve error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ReadDirectory(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
}

func TestConcurrentRequestsAreIndependent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	provider := source{read: func(ctx context.Context, path string) ([]core.FileEntry, error) {
		if path == "slow" {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []core.FileEntry{{Name: path}}, nil
	}}
	service := app.NewDirectoryService(provider, provider, "fast")
	finished := make(chan error, 1)
	go func() { _, err := service.ReadDirectory(ctx, "slow"); finished <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("slow request did not start")
	}
	result, err := service.ReadDirectory(ctx, "fast")
	if err != nil || result.Path != "fast" || result.Entries[0].Name != "fast" {
		t.Fatalf("independent request = %+v, %v", result, err)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("slow request: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read did not stop after cancellation")
	}
}

func TestLocalDirectoryNavigation(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "каталог с пробелами")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"visible.txt", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	filesystem := localfs.LocalFileSystem{}
	service := app.NewDirectoryService(filesystem, filesystem, dir)
	result, err := service.ReadDirectory(context.Background(), "")
	if err != nil || len(result.Entries) != 3 {
		t.Fatalf("initial directory: %+v, %v", result, err)
	}
	for _, entry := range result.Entries {
		if entry.Hidden != (entry.Name == ".hidden") {
			t.Errorf("hidden flag: %+v", entry)
		}
	}
	result, err = service.ReadDirectory(context.Background(), child)
	if err != nil || result.Entries == nil || len(result.Entries) != 0 {
		t.Fatalf("child: %+v, %v", result, err)
	}
	parent, err := service.ReadDirectory(context.Background(), result.Parent)
	if err != nil || len(parent.Entries) != 3 {
		t.Fatalf("parent: %+v, %v", parent, err)
	}
	root := filepath.VolumeName(dir) + string(os.PathSeparator)
	result, err = service.ReadDirectory(context.Background(), root)
	if err != nil || result.Parent != result.Path {
		t.Fatalf("root parent: %+v, %v", result, err)
	}
}
