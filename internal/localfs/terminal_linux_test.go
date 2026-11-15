//go:build linux

package localfs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/settings"
)

func TestTerminalAdapters(t *testing.T) {
	directory := "/tmp/папка 'quoted'; $HOME"
	command := `printf '%s\n' 'a;b' "привет мир" | cat > "out file"`
	for _, test := range []struct {
		name   string
		prefix []string
	}{
		{"xdg-terminal-exec", []string{"--dir=" + directory, "--"}},
		{"gnome-terminal", []string{"--window", "--working-directory=" + directory, "--"}},
		{"ptyxis", []string{"--standalone", "--new-window", "--working-directory=" + directory, "--"}},
		{"konsole", []string{"--separate", "--workdir", directory, "-e"}},
		{"xfce4-terminal", []string{"--disable-server", "--window", "--working-directory=" + directory, "--execute"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := settings.TerminalConfig{Mode: "custom", Executable: "/opt/Terminal App", Adapter: test.name, Shell: "/bin/bash", Arguments: []string{"literal;argument"}, ShellArguments: []string{"--noprofile"}}
			for _, hold := range []bool{false, true} {
				c.Hold = hold
				err := launchTerminal(c, command, directory, func(s string) (string, error) { return s, nil }, func(exe string, args []string, dir string) error {
					want := append([]string{"literal;argument"}, test.prefix...)
					if hold {
						want = append(want, "/bin/sh", "-c", terminalHoldScript, "marten-terminal")
					}
					want = append(want, "/bin/bash", "--noprofile", "-c", command)
					if exe != c.Executable || dir != directory || !reflect.DeepEqual(args, want) {
						t.Fatalf("%q %#v %q", exe, args, dir)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestTerminalDiscoveryAndErrors(t *testing.T) {
	c := settings.TerminalConfig{Mode: "auto", Shell: "/bin/bash"}
	names := []string{"xdg-terminal-exec", "gnome-terminal", "ptyxis", "konsole", "xfce4-terminal"}
	for index, want := range names {
		err := launchTerminal(c, "pwd", "/tmp", func(name string) (string, error) {
			if name == c.Shell {
				return name, nil
			}
			for _, n := range names[index:] {
				if name == n {
					return "/usr/bin/" + name, nil
				}
			}
			return "", os.ErrNotExist
		}, func(exe string, _ []string, _ string) error {
			if exe != "/usr/bin/"+want {
				t.Fatal(exe)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	never := func(string, []string, string) error { t.Fatal("unexpected launch"); return nil }
	missing := func(string) (string, error) { return "", os.ErrNotExist }
	if err := launchTerminal(c, "pwd", "/tmp", missing, never); err == nil {
		t.Fatal("missing terminal accepted")
	}
	c.Mode = "custom"
	c.Executable = "missing"
	c.Adapter = "gnome-terminal"
	if err := launchTerminal(c, "pwd", "/tmp", missing, never); err == nil {
		t.Fatal("invalid custom accepted")
	}
	sentinel := errors.New("start failed")
	if err := launchTerminal(c, "pwd", "/tmp", func(s string) (string, error) { return s, nil }, func(string, []string, string) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

// Execute only the payload of the fake terminal, exercising real shell syntax
// without opening a GUI or letting the GUI launcher invoke a shell itself.
func TestTerminalShellPayload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Каталог с пробелами; 'quote'")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	for _, test := range []struct{ command, input, want string }{
		{"pwd", "", dir}, {"git status --short", "", ""}, {"ls -la", "", ".git"},
		{`printf '%s\n' 'a;b' "два слова"`, "", "a;b\nдва слова"},
		{`printf '%s' "$HOME" | cat > 'выход файл'; cat 'выход файл'`, "", os.Getenv("HOME")},
		{`printf a & wait`, "", "a"},
		{`read -r answer; printf '%s' "$answer"`, "interactive input\n", "interactive input"},
	} {
		t.Run(test.command, func(t *testing.T) {
			c := settings.TerminalConfig{Mode: "custom", Executable: "gnome-terminal", Adapter: "gnome-terminal", Shell: "/bin/bash"}
			err := launchTerminal(c, test.command, dir, func(s string) (string, error) { return s, nil }, func(_ string, args []string, cwd string) error {
				payload := args[3:]
				cmd := exec.Command(payload[0], payload[1:]...)
				cmd.Dir = cwd
				cmd.Stdin = strings.NewReader(test.input)
				out, err := cmd.CombinedOutput()
				if err != nil {
					return err
				}
				if !strings.Contains(string(out), test.want) {
					t.Fatalf("output %q want %q", out, test.want)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHoldSupervisorPreservesExitAndCommand(t *testing.T) {
	// /dev/tty is deliberately unavailable in this non-interactive unit test;
	// the supervisor must still preserve the user's status and print it.
	cmd := exec.Command("/bin/sh", "-c", terminalHoldScript, "marten-terminal", "/bin/bash", "-c", `printf '%s' 'literal; $HOME'; exit 7`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 7 || !strings.Contains(string(out), "literal; $HOME") || !strings.Contains(string(out), "Exit status: 7") {
		t.Fatalf("%s %v", out, err)
	}
}

func TestHoldAndInteractiveReadWithPTY(t *testing.T) {
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("util-linux script is needed for the PTY integration test")
	}
	// script creates a real PTY without a GUI. Quoting here is exclusively a test
	// harness requirement; production passes these strings as separate argv.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	command := "/bin/sh -c " + quote(terminalHoldScript) + " marten-terminal /bin/bash -c " + quote(`read -r answer; printf 'received:%s\n' "$answer"; exit 7`)
	cmd := exec.Command(script, "-q", "-e", "-c", command, "/dev/null")
	cmd.Stdin = strings.NewReader("interactive input\n\n")
	out, err := cmd.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 7 || !strings.Contains(string(out), "received:interactive input") || !strings.Contains(string(out), "Exit status: 7") {
		t.Fatalf("PTY output %q error %v", out, err)
	}
}
