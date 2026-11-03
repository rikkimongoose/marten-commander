package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
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
	service := NewDirectoryService(app.NewDirectoryService(provider, provider, dir))
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
	service := NewDirectoryService(app.NewDirectoryService(provider, provider, t.TempDir()))
	for _, request := range []DirectoryRequest{{Location: "!invalid"}, {Path: "/tmp", Location: encode("/tmp")}} {
		if _, err := service.ReadDirectory(context.Background(), request); err == nil {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	}
	provider.err = fs.ErrPermission
	_, err := service.ReadDirectory(context.Background(), DirectoryRequest{})
	if err == nil || !strings.Contains(err.Error(), "Не удалось прочитать каталог") || !strings.Contains(err.Error(), fs.ErrPermission.Error()) {
		t.Fatalf("unhelpful error: %v", err)
	}
	provider.err = errors.New("failure\nwith control")
	_, err = service.ReadDirectory(context.Background(), DirectoryRequest{})
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("unescaped error: %q", err)
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
