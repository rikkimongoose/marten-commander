package volumes

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type fakeBackend struct {
	items                        []Volume
	value                        Space
	listErr, locateErr, spaceErr error
	seen                         string
	spaceCalls                   int
}

func (f *fakeBackend) list(context.Context) ([]Volume, error) { return f.items, f.listErr }
func (f *fakeBackend) locate(_ context.Context, path string) (Volume, error) {
	f.seen = path
	if f.locateErr != nil {
		return Volume{}, f.locateErr
	}
	return Volume{ID: "root", Name: "Disk", Root: filepath.VolumeName(path) + string(filepath.Separator)}, nil
}
func (f *fakeBackend) space(string) (Space, error) { f.spaceCalls++; return f.value, f.spaceErr }

func TestSpaceCounts(t *testing.T) {
	s, err := spaceFromCounts(100, 40, 30, 4096)
	if err != nil || s != (Space{409600, 163840, 122880, 245760}) {
		t.Fatalf("wrong counters: %+v, %v", s, err)
	}
	for _, v := range [][4]uint64{{100, 101, 20, 1}, {100, 10, 20, 1}, {math.MaxUint64, 0, 0, 4096}, {100, 0, 0, 0}} {
		if _, err := spaceFromCounts(v[0], v[1], v[2], v[3]); err == nil {
			t.Fatalf("accepted bad counters: %v", v)
		}
	}
	if s, err := spaceFromCounts(0, 0, 0, 4096); err != nil || s.Total != 0 {
		t.Fatalf("zero capacity: %+v %v", s, err)
	}
}

func TestHomeIsSeparateAndUsesContainingFilesystem(t *testing.T) {
	home := t.TempDir()
	fake := &fakeBackend{items: []Volume{{ID: "root", Root: filepath.VolumeName(home) + string(filepath.Separator)}}, value: Space{100, 40, 30, 60}}
	service := &Service{backend: fake, home: func() (string, error) { return home, nil }}
	items, err := service.ListVolumes(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != "root" {
		t.Fatalf("Home leaked into volumes: %+v %v", items, err)
	}
	if fake.spaceCalls != 0 {
		t.Fatal("enumeration probed devices")
	}
	path, err := service.GetHomeDirectory(context.Background())
	if err != nil || path != home {
		t.Fatalf("home: %s %v", path, err)
	}
	volume, err := service.GetVolumeInfo(context.Background(), path)
	if err != nil || volume.ID != "root" || volume.Space == nil || volume.Space.Available != 30 {
		t.Fatalf("home capacity: %+v %v", volume, err)
	}
}

func TestUnavailableVolumesAndCapacity(t *testing.T) {
	path := t.TempDir()
	fake := &fakeBackend{spaceErr: fs.ErrPermission}
	service := &Service{backend: fake, home: func() (string, error) { return "", fs.ErrNotExist }}
	volume, err := service.GetVolumeInfo(context.Background(), path)
	if err != nil || volume.Space != nil || volume.Warning == "" {
		t.Fatalf("missing capacity must remain unknown: %+v %v", volume, err)
	}
	fake.locateErr = fs.ErrNotExist
	if _, err := service.GetVolumeInfo(context.Background(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing mount: %v", err)
	}
	if fake.spaceCalls != 1 {
		t.Fatal("read capacity of unavailable volume")
	}
	fake.listErr = fs.ErrPermission
	if _, err := service.ListVolumes(context.Background()); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("listing: %v", err)
	}
	if _, err := service.GetHomeDirectory(context.Background()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("home: %v", err)
	}
	service.home = func() (string, error) { return "relative", nil }
	if _, err := service.GetHomeDirectory(context.Background()); err == nil {
		t.Fatal("relative Home accepted")
	}
}

func TestPathsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBackend{}
	service := &Service{backend: fake, home: func() (string, error) { return dir, nil }}
	if _, err := service.GetVolumeInfo(context.Background(), file); err != nil || fake.seen != file {
		t.Fatalf("file lookup: %s %v", fake.seen, err)
	}
	for _, path := range []string{"", "relative", dir + string(rune(0)), filepath.Join(dir, "missing")} {
		if _, err := service.GetVolumeInfo(context.Background(), path); err == nil {
			t.Fatalf("accepted invalid path %q", path)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ListVolumes(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := service.GetVolumeInfo(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := service.GetHomeDirectory(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fake.items = nil
	items, err := service.ListVolumes(context.Background())
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty list: %#v %v", items, err)
	}
}
