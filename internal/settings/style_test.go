package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStyle(t *testing.T, contents string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "style.yaml")
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestStyleConfigMatchesCaseInsensitiveMultipleMasksAndFirstRule(t *testing.T) {
	filename := writeStyle(t, `style:
  - mask: ['*.jpg', '*.jpe', '\*.jpeg']
    color: '#00aaff'
  - mask: ['photo.*', '*.zip', '*.7z']
    color: '#805AD5'
`)
	config, err := LoadStyleConfig(filename)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PHOTO.JPG", "image.JpE", "снимок.JPEG"} {
		if color, ok := config.Color(name); !ok || color != "#00AAFF" {
			t.Errorf("%q: %q, %v", name, color, ok)
		}
	}
	if color, ok := config.Color("photo.jpg"); !ok || color != "#00AAFF" {
		t.Fatalf("first rule priority = %q, %v", color, ok)
	}
	if color, ok := config.Color("archive.7Z"); !ok || color != "#805AD5" {
		t.Fatalf("second rule = %q, %v", color, ok)
	}
	if _, ok := config.Color("picture.png"); ok {
		t.Fatal("unmatched file received a colour")
	}
	if _, ok := config.Color("directory/photo.jpg"); ok {
		t.Fatal("matching unexpectedly accepted a path instead of a full basename")
	}
}

func TestStyleConfigMissingAndEmptyUseDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	for _, filename := range []string{missing, writeStyle(t, ""), writeStyle(t, "style: []\n"), writeStyle(t, "{}\n")} {
		config, err := LoadStyleConfig(filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := config.Color("file.jpg"); ok {
			t.Fatalf("%q did not use default style", filename)
		}
	}
}

func TestStyleConfigRejectsWholeFileOnYAMLMaskOrColorError(t *testing.T) {
	for _, test := range []struct{ name, contents string }{
		{"yaml", "style: ["},
		{"unknown field", "style:\n  - mask: ['*.jpg']\n    colour: '#00AAFF'\n"},
		{"empty masks", "style:\n  - mask: []\n    color: '#00AAFF'\n"},
		{"bad mask", "style:\n  - mask: ['[jpg']\n    color: '#00AAFF'\n"},
		{"path mask", "style:\n  - mask: ['dir/*.jpg']\n    color: '#00AAFF'\n"},
		{"bad color", "style:\n  - mask: ['*.jpg']\n    color: 'blue'\n"},
		{"two documents", "style: []\n---\nstyle: []\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := LoadStyleConfig(writeStyle(t, test.contents))
			if err == nil || !strings.Contains(err.Error(), "style") {
				t.Fatalf("config=%#v error=%v", config, err)
			}
			if _, ok := config.Color("file.jpg"); ok {
				t.Fatal("partially parsed rules remained active")
			}
		})
	}
}
