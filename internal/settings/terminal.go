package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// TerminalConfig uses tokenised arguments, like EditorConfig. Adapter is required
// in custom mode so executable names never imply an argument protocol.
type TerminalConfig struct {
	Mode           string
	Executable     string
	Adapter        string
	Arguments      []string
	Shell          string
	ShellArguments []string
	Hold           bool
}

func LoadTerminalConfig() (TerminalConfig, error) {
	c := TerminalConfig{Mode: "auto", Shell: "/bin/bash", Hold: true}
	for key, target := range map[string]*string{"MODE": &c.Mode, "EXECUTABLE": &c.Executable, "ADAPTER": &c.Adapter, "SHELL": &c.Shell} {
		if v := os.Getenv("MARTEN_COMMANDER_TERMINAL_" + key); v != "" {
			*target = v
		}
	}
	for key, target := range map[string]*[]string{"ARGUMENTS": &c.Arguments, "SHELL_ARGUMENTS": &c.ShellArguments} {
		if v := os.Getenv("MARTEN_COMMANDER_TERMINAL_" + key); v != "" {
			if err := json.Unmarshal([]byte(v), target); err != nil || *target == nil {
				return c, fmt.Errorf("%s должен быть JSON-массивом строк", key)
			}
		}
	}
	switch v := os.Getenv("MARTEN_COMMANDER_TERMINAL_HOLD"); v {
	case "", "true":
	case "false":
		c.Hold = false
	default:
		return c, fmt.Errorf("TERMINAL_HOLD должен быть true или false")
	}
	return c, ValidateTerminalConfig(c)
}

func ValidateTerminalConfig(c TerminalConfig) error {
	if c.Mode != "auto" && c.Mode != "custom" {
		return fmt.Errorf("неизвестный режим терминала %q", c.Mode)
	}
	if c.Shell == "" {
		return fmt.Errorf("не указана оболочка")
	}
	if c.Mode == "custom" {
		if c.Executable == "" {
			return fmt.Errorf("не указан исполняемый файл терминала")
		}
		switch c.Adapter {
		case "xdg-terminal-exec", "gnome-terminal", "ptyxis", "konsole", "xfce4-terminal":
		default:
			return fmt.Errorf("неизвестный адаптер терминала %q", c.Adapter)
		}
	}
	for _, s := range append(append([]string{c.Executable, c.Shell}, c.Arguments...), c.ShellArguments...) {
		if strings.ContainsRune(s, 0) {
			return fmt.Errorf("настройка терминала содержит NUL")
		}
	}
	return nil
}
