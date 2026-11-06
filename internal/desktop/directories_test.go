package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/core"
)

type source struct {
	core.FileSystem
	entries []core.FileEntry
	seen    string
	err     error
}

func (s *source) ReadDir(_ context.Context, path string) ([]core.FileEntry, error) {
	s.seen = path
	return s.entries, s.err
}
func (s *source) ResolvePath(_ context.Context, path string) (string, error) { return path, nil }

func TestDTOAndOpaquePathsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw-\xff\n")
	provider := &source{entries: []core.FileEntry{
		{Path: raw, Name: "raw-\xff\n", Kind: core.KindDirectory},
		{Path: filepath.Join(dir, "big.txt"), Name: "big.txt", Kind: core.KindFile, Size: 9007199254740993,
			ModifiedAt: time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)},
		{Path: filepath.Join(dir, ".hidden"), Name: ".hidden", Kind: core.KindFile, Hidden: true},
	}}
	service := NewDirectoryService(app.NewDirectoryService(provider, provider, dir), false)
	result, err := service.ReadDirectory(context.Background(), DirectoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entries[1].Size != "9007199254740993" || result.Entries[1].Extension != "txt" || result.Entries[1].ModifiedAt != "2026-01-02T03:04:05.123456789Z" {
		t.Fatalf("lossy DTO: %+v", result.Entries[1])
	}
	if !result.Entries[2].Hidden || result.Entries[2].Extension != "" {
		t.Fatalf("dotfile: %+v", result.Entries[2])
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Directory
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(decoded.Entries[0].Name) || strings.Contains(decoded.Entries[0].Name, "\n") {
		t.Fatalf("unsafe display name: %q", decoded.Entries[0].Name)
	}
	if _, err := service.ReadDirectory(context.Background(), DirectoryRequest{Location: decoded.Entries[0].ID}); err != nil {
		t.Fatal(err)
	}
	if provider.seen != raw {
		t.Fatalf("path bytes changed: %q != %q", provider.seen, raw)
	}
	if _, err := service.ReadDirectory(context.Background(), DirectoryRequest{Location: result.ParentLocation}); err != nil {
		t.Fatal(err)
	}
	if provider.seen != filepath.Dir(dir) {
		t.Fatalf("wrong parent: %q", provider.seen)
	}
}

func TestInvalidRequestAndErrors(t *testing.T) {
	provider := &source{}
	service := NewDirectoryService(app.NewDirectoryService(provider, provider, t.TempDir()), false)
	for _, request := range []DirectoryRequest{{Location: "!invalid"}, {Path: "/tmp", Location: encode("/tmp")}} {
		if _, err := service.ReadDirectory(context.Background(), request); err == nil {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	}
	provider.err = fs.ErrPermission
	_, err := service.ReadDirectory(context.Background(), DirectoryRequest{})
	if err == nil || !strings.Contains(err.Error(), "Нет прав на чтение каталога") {
		t.Fatalf("unhelpful error: %v", err)
	}
	provider.err = errors.New("failure\nwith control")
	_, err = service.ReadDirectory(context.Background(), DirectoryRequest{})
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("unescaped error: %q", err)
	}
}

func TestDirectoryAncestorsUseResolvedOpaqueAddresses(t *testing.T) {
	base := t.TempDir()
	// Names are intentionally unsuitable for comparisons against display paths.
	path := filepath.Join(base, "home-\xff", "child\nname")
	provider := &source{}
	service := NewDirectoryService(app.NewDirectoryService(provider, provider, base), false)
	result, err := service.ReadDirectory(context.Background(), DirectoryRequest{Location: encode(path)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AncestorLocations) < 2 || result.AncestorLocations[0] != encode(filepath.Dir(path)) || result.AncestorLocations[1] != encode(base) {
		t.Fatalf("wrong ancestry: %+v", result)
	}
	root := filepath.VolumeName(base) + string(filepath.Separator)
	if result.AncestorLocations[len(result.AncestorLocations)-1] != encode(root) {
		t.Fatalf("ancestry must end at root: %+v", result)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Directory
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded.AncestorLocations, result.AncestorLocations) {
		t.Fatalf("ancestry lost in transport: %+v, %v", decoded, err)
	}
	result, err = service.ReadDirectory(context.Background(), DirectoryRequest{Path: root})
	if err != nil || result.AncestorLocations == nil || len(result.AncestorLocations) != 0 {
		t.Fatalf("root must have an empty ancestor list: %+v, %v", result, err)
	}
}

func TestPermissionDeniedMessageAndRecovery(t *testing.T) {
	path := filepath.FromSlash("/boot/efi")
	provider := &source{err: &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}}
	service := NewDirectoryService(app.NewDirectoryService(provider, provider, path), false)
	for _, request := range []DirectoryRequest{{}, {Path: path}, {Location: encode(path)}} {
		result, err := service.ReadDirectory(context.Background(), request)
		if err == nil || err.Error() != "Нет прав на чтение каталога "+display(path) || result.Location != "" || result.Entries != nil {
			t.Fatalf("permission failure: %+v, %v", result, err)
		}
	}
	provider.err = nil
	if _, err := service.ReadDirectory(context.Background(), DirectoryRequest{}); err != nil {
		t.Fatalf("service must remain usable after a failed read: %v", err)
	}
}

func TestInitialHomeFlagDoesNotLabelFallbackOrOrdinaryNavigation(t *testing.T) {
	path := t.TempDir()
	provider := &source{}
	for _, initialIsHome := range []bool{true, false} {
		service := NewDirectoryService(app.NewDirectoryService(provider, provider, path), initialIsHome)
		for _, request := range []DirectoryRequest{{}, {Path: path}, {Location: encode(path)}} {
			result, err := service.ReadDirectory(context.Background(), request)
			wantHome := initialIsHome && request.Path == "" && request.Location == ""
			if err != nil || result.InitialHome != wantHome || result.Location != encode(path) {
				t.Fatalf("initialIsHome=%v request=%+v: %+v, %v", initialIsHome, request, result, err)
			}
		}
	}
}

func TestDisplayEscapingDoesNotMergeDistinctNames(t *testing.T) {
	seen := map[string]bool{}
	for _, raw := range []string{"bad-\xff", "bad-\xfe", "bad-\\xff", "line\nbreak", "line\\nbreak", "Unicode 日本語", "control\u202e"} {
		text := display(raw)
		if !utf8.ValidString(text) || seen[text] {
			t.Fatalf("ambiguous display: %q -> %q", raw, text)
		}
		seen[text] = true
	}
}
