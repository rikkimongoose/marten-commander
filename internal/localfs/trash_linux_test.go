//go:build linux

package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

func TestTrashSafeArgvAndObjects(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, kind string }{{"empty", "file"}, {"Привет мир; $(touch bad) 'quote'", "file"}, {"--option", "file"}, {"empty dir", "directory"}, {"tree", "tree"}, {"link", "symlink"}} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name)
			switch test.kind {
			case "file":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory", "tree":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if test.kind == "tree" {
					os.WriteFile(filepath.Join(path, "child"), []byte("content"), 0600)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			source, err := (SystemTrash{}).InspectTrash(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(t.TempDir(), test.name)
			err = trashWith(context.Background(), source, func(s string) (string, error) {
				if s != "gio" {
					t.Fatal(s)
				}
				return "/usr/bin/gio", nil
			}, func(exe string, args []string) ([]byte, error) {
				if exe != "/usr/bin/gio" || !reflect.DeepEqual(args, []string{"trash", "--", path}) {
					t.Fatalf("%s %q", exe, args)
				}
				return nil, os.Rename(path, moved)
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(moved); err != nil {
				t.Fatal(err)
			}
			if test.kind == "tree" {
				if b, err := os.ReadFile(filepath.Join(moved, "child")); err != nil || string(b) != "content" {
					t.Fatal(err)
				}
			}
			if test.kind == "symlink" {
				if source.Kind != core.KindSymlink {
					t.Fatal(source)
				}
				if link, err := os.Readlink(moved); err != nil || link != target {
					t.Fatal(link, err)
				}
			}
		})
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatal("link target changed", err)
	}
}

func TestTrashRefusesProtectedAndChangedSources(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	t.Setenv("XDG_DATA_HOME", data)
	trash := filepath.Join(data, "Trash")
	os.MkdirAll(filepath.Join(trash, "files"), 0700)
	os.MkdirAll(filepath.Join(dir, ".Trash-1000", "files"), 0700)
	alias := filepath.Join(dir, "alias")
	os.Symlink(trash, alias)
	for _, path := range []string{"/", "/proc", "/dev/null", "relative", dir, data, trash, filepath.Join(trash, "files"), filepath.Join(alias, "files"), filepath.Join(dir, ".Trash-1000"), dir + "/.."} {
		if _, err := (SystemTrash{}).InspectTrash(context.Background(), path); err == nil {
			t.Errorf("accepted protected %q", path)
		}
	}
	path := filepath.Join(dir, "file")
	os.WriteFile(path, []byte("original"), 0600)
	source, err := (SystemTrash{}).InspectTrash(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	os.Rename(path, path+".old")
	os.WriteFile(path, []byte("replacement"), 0600)
	err = trashWith(context.Background(), source, func(string) (string, error) { return "gio", nil }, func(string, []string) ([]byte, error) { t.Fatal("changed source launched"); return nil, nil })
	if !errors.Is(err, core.ErrSourceChanged) {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "replacement" {
		t.Fatal("replacement changed")
	}
}

func TestTrashFailureNeverFallsBack(t *testing.T) {
	for _, failure := range []error{os.ErrPermission, os.ErrNotExist, errors.New("read-only filesystem"), errors.New("trash not supported on mount")} {
		dir := t.TempDir()
		path := filepath.Join(dir, "keep")
		os.WriteFile(path, []byte("intact"), 0600)
		source, err := (SystemTrash{}).InspectTrash(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		err = trashWith(context.Background(), source, func(string) (string, error) { return "gio", nil }, func(_ string, args []string) ([]byte, error) {
			calls++
			if args[0] != "trash" || len(args) != 3 {
				t.Fatal(args)
			}
			return []byte("failure"), failure
		})
		if err == nil || calls != 1 {
			t.Fatal(err, calls)
		}
		if b, _ := os.ReadFile(path); string(b) != "intact" {
			t.Fatal("destructive fallback")
		}
		err = trashWith(context.Background(), source, func(string) (string, error) { return "", os.ErrNotExist }, func(string, []string) ([]byte, error) { t.Fatal("missing gio launched"); return nil, nil })
		if err == nil {
			t.Fatal("missing gio accepted")
		}
		err = trashWith(context.Background(), source, func(string) (string, error) { return "gio", nil }, func(string, []string) ([]byte, error) { return nil, nil })
		if err == nil {
			t.Fatal("successful start without movement accepted")
		}
		os.Rename(path, path+".gone")
		err = trashWith(context.Background(), source, func(string) (string, error) { return "gio", nil }, func(string, []string) ([]byte, error) { t.Fatal("missing source launched"); return nil, nil })
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func TestTrashProtectsConfiguredTrashSymlink(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	storage := filepath.Join(dir, "storage")
	os.Mkdir(data, 0700)
	os.Mkdir(storage, 0700)
	trash := filepath.Join(data, "Trash")
	os.Symlink(storage, trash)
	t.Setenv("XDG_DATA_HOME", data)
	for _, path := range []string{trash, storage, data} {
		if _, err := (SystemTrash{}).InspectTrash(context.Background(), path); err == nil {
			t.Errorf("configured trash infrastructure accepted: %q", path)
		}
	}
}
