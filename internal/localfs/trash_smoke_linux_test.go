//go:build linux

package localfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in only: tests our real adapter against the user's GIO trash, using only
// newly created fixtures. Restored fixtures remain under ~/marten-trash-test.
func TestTrashGIOSmoke(t *testing.T) {
	if os.Getenv("MARTEN_TRASH_SMOKE") != "1" {
		t.Skip("set MARTEN_TRASH_SMOKE=1 in a desktop session")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(home, "marten-trash-test")
	if root := os.Getenv("MARTEN_TRASH_SMOKE_ROOT"); root != "" {
		base = filepath.Join(root, "marten-trash-test")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("test fixtures: %s", dir)
	adapter := SystemTrash{}
	trash := func(path string) {
		t.Helper()
		source, err := adapter.InspectTrash(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Trash(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, "target.txt")
	os.WriteFile(target, []byte("untouched target"), 0600)
	duplicate := filepath.Join(dir, "Привет мир; 'quote'.txt")
	for _, content := range []string{"first", "second"} {
		if err := os.WriteFile(duplicate, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		trash(duplicate)
	}
	tree := filepath.Join(dir, "tree")
	os.MkdirAll(filepath.Join(tree, "nested"), 0700)
	os.WriteFile(filepath.Join(tree, "nested", "file"), []byte("tree content"), 0600)
	trash(tree)
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	trash(link)
	empty := filepath.Join(dir, "empty-dir")
	os.Mkdir(empty, 0700)
	trash(empty)
	zero := filepath.Join(dir, "empty-file")
	os.WriteFile(zero, nil, 0600)
	trash(zero)
	cmd := exec.Command("gio", "trash", "--list")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list: %v %s", err, out)
	}
	count := 0
	contents := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, dir+"/") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "trash://") {
			t.Fatalf("unexpected GIO list format: %q", line)
		}
		uri := fields[0]
		count++
		if output, err := exec.Command("gio", "trash", "--restore", "--", uri).CombinedOutput(); err != nil {
			t.Fatalf("restore %s: %v %s", uri, err, output)
		}
		if _, err := os.Lstat(duplicate); err == nil {
			b, err := os.ReadFile(duplicate)
			if err != nil {
				t.Fatal(err)
			}
			contents[string(b)] = true
			if err := os.Rename(duplicate, filepath.Join(dir, fmt.Sprintf("restored-duplicate-%d.txt", count))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count != 6 || !contents["first"] || !contents["second"] {
		t.Fatalf("restored=%d duplicate contents=%v", count, contents)
	}
	if b, err := os.ReadFile(filepath.Join(tree, "nested", "file")); err != nil || string(b) != "tree content" {
		t.Fatal("tree restore", err)
	}
	if destination, err := os.Readlink(link); err != nil || destination != target {
		t.Fatal("link restore", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "untouched target" {
		t.Fatal("target changed", err)
	}
	if info, err := os.Stat(empty); err != nil || !info.IsDir() {
		t.Fatal("empty directory restore", err)
	}
	if b, err := os.ReadFile(zero); err != nil || len(b) != 0 {
		t.Fatal("empty file restore", err)
	}
	t.Log("GIO confirmed all six entries with original paths; restore, duplicate names, tree and symlink verified")
}
