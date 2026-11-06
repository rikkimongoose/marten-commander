package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/volumes"
)

type volumeSource struct {
	seen string
	err  error
}

func (s *volumeSource) ListVolumes(context.Context) ([]volumes.Volume, error) {
	return []volumes.Volume{{ID: "disk", Name: "Data\n", Root: "/raw-\xff", Space: &volumes.Space{Total: 9007199254740993, Free: 10, Available: 5, Used: 9007199254740983}}}, s.err
}
func (s *volumeSource) GetVolumeInfo(_ context.Context, path string) (volumes.Volume, error) {
	s.seen = path
	return volumes.Volume{ID: "disk", Root: path}, s.err
}
func (s *volumeSource) GetHomeDirectory(context.Context) (string, error) { return "/home/user", s.err }

func TestVolumeDTOAndHome(t *testing.T) {
	source := &volumeSource{}
	service := NewVolumeService(source)
	items, err := service.ListVolumes(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %+v %v", items, err)
	}
	if items[0].Space.Total != "9007199254740993" || strings.Contains(items[0].Name, "\n") {
		t.Fatalf("lossy DTO: %+v", items[0])
	}
	data, _ := json.Marshal(items)
	var decoded []Volume
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	info, err := service.GetVolumeInfo(context.Background(), DirectoryRequest{Location: decoded[0].RootLocation})
	if err != nil || source.seen != "/raw-\xff" || info.Space != nil {
		t.Fatalf("native bytes or unknown capacity lost: %+v %q %v", info, source.seen, err)
	}
	home, err := service.GetHomeDirectory(context.Background())
	if err != nil || home.Location != encode("/home/user") || home.DisplayPath != "/home/user" {
		t.Fatalf("Home: %+v %v", home, err)
	}
}

func TestVolumeTransportErrors(t *testing.T) {
	service := NewVolumeService(&volumeSource{err: errors.New("failed\nread")})
	if _, err := service.ListVolumes(context.Background()); err == nil || strings.Contains(err.Error(), "\n") {
		t.Fatalf("list error: %v", err)
	}
	if _, err := service.GetHomeDirectory(context.Background()); err == nil {
		t.Fatal("Home failure lost")
	}
	for _, request := range []DirectoryRequest{{Path: "/tmp"}, {Location: "!bad"}, {Path: "/tmp", Location: encode("/tmp")}} {
		if _, err := service.GetVolumeInfo(context.Background(), request); err == nil {
			t.Fatalf("error lost: %+v", request)
		}
	}
}
