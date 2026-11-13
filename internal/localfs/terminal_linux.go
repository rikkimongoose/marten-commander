//go:build linux

package localfs

import (
	"fmt"
	"os/exec"

	"github.com/rikkimongoose/marten-commander/internal/settings"
)

// This fixed supervisor runs inside the terminal. The user's command remains
// one argument of the selected shell, never source text in the supervisor.
const terminalHoldScript = `"$@"
status=$?
printf '\n[Exit status: %s] Press Enter to close…' "$status"
IFS= read -r answer </dev/tty
exit "$status"`

func (SystemTerminalLauncher) LaunchTerminal(c settings.TerminalConfig, command, directory string) error {
	return launchTerminal(c, command, directory, exec.LookPath, startTerminal)
}

func launchTerminal(c settings.TerminalConfig, command, directory string, lookup func(string) (string, error), start func(string, []string, string) error) error {
	if err := settings.ValidateTerminalConfig(c); err != nil {
		return err
	}
	executable, adapter := c.Executable, c.Adapter
	if c.Mode == "auto" {
		executable, adapter = "", ""
		for _, name := range []string{"xdg-terminal-exec", "gnome-terminal", "ptyxis", "konsole", "xfce4-terminal"} {
			if path, err := lookup(name); err == nil {
				executable, adapter = path, name
				break
			}
		}
		if adapter == "" {
			return fmt.Errorf("совместимый терминал не найден; установите GNOME Terminal или настройте custom")
		}
	} else {
		path, err := lookup(executable)
		if err != nil {
			return fmt.Errorf("терминал %q недоступен: %w", executable, err)
		}
		executable = path
	}
	shell, err := lookup(c.Shell)
	if err != nil {
		return fmt.Errorf("оболочка %q недоступна: %w", c.Shell, err)
	}
	payload := append([]string{shell}, c.ShellArguments...)
	payload = append(payload, "-c", command)
	if c.Hold {
		payload = append([]string{"/bin/sh", "-c", terminalHoldScript, "marten-terminal"}, payload...)
	}
	args := append([]string(nil), c.Arguments...)
	switch adapter {
	case "xdg-terminal-exec":
		args = append(args, "--dir="+directory, "--")
	case "gnome-terminal":
		args = append(args, "--window", "--working-directory="+directory, "--")
	case "ptyxis":
		args = append(args, "--standalone", "--new-window", "--working-directory="+directory, "--")
	case "konsole":
		args = append(args, "--separate", "--workdir", directory, "-e")
	case "xfce4-terminal":
		args = append(args, "--disable-server", "--window", "--working-directory="+directory, "--execute")
	}
	return start(executable, append(args, payload...), directory)
}
