//go:build linux

package localfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/settings"
)

// Opt-in integration test: opens real terminal windows, never runs in CI by default.
func TestTerminalGraphicalSmoke(t *testing.T) {
	if os.Getenv("MARTEN_TERMINAL_SMOKE") != "1" {
		t.Skip("set MARTEN_TERMINAL_SMOKE=1 in a graphical Linux session")
	}
	for _, adapter := range []string{"auto", "gnome-terminal", "ptyxis"} {
		t.Run(adapter, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "Пример 'quotes'; directory")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			c := settings.TerminalConfig{Mode: "custom", Executable: adapter, Adapter: adapter, Shell: "/bin/bash"}
			if adapter == "auto" {
				c.Mode = "auto"
				c.Adapter = ""
			}
			// Relative output paths prove that the terminal server received cwd. The
			// stdin test proves interactive programs receive a terminal, not a pipe.
			command := `{ pwd; git init -q; git status --short; ls -la; printf '%s\n' 'a;b' 'Привет мир' | cat; test -t 0 && printf 'TTY_OK\n'; } > result.txt 2>&1; printf done > finished`
			if err := (SystemTerminalLauncher{}).LaunchTerminal(c, command, dir); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(dir, "finished")); err == nil {
					out, err := os.ReadFile(filepath.Join(dir, "result.txt"))
					if err != nil {
						t.Fatal(err)
					}
					for _, want := range []string{dir, ".git", "a;b", "Привет мир", "TTY_OK"} {
						if !strings.Contains(string(out), want) {
							t.Fatalf("missing %q: %s", want, out)
						}
					}
					t.Logf("%s: cwd, git status, ls, pipeline, Unicode and TTY verified", adapter)
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatal("terminal did not produce result within 10 seconds")
		})
	}
}
