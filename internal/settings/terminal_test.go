package settings

import "testing"

func TestTerminalSettings(t *testing.T) {
	for _, key := range []string{"MODE", "EXECUTABLE", "ADAPTER", "ARGUMENTS", "SHELL", "SHELL_ARGUMENTS", "HOLD"} {
		t.Setenv("MARTEN_COMMANDER_TERMINAL_"+key, "")
	}
	c, err := LoadTerminalConfig()
	if err != nil || c.Mode != "auto" || c.Shell != "/bin/bash" || !c.Hold {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("MARTEN_COMMANDER_TERMINAL_MODE", "custom")
	t.Setenv("MARTEN_COMMANDER_TERMINAL_EXECUTABLE", "/opt/Terminal App")
	t.Setenv("MARTEN_COMMANDER_TERMINAL_ADAPTER", "gnome-terminal")
	t.Setenv("MARTEN_COMMANDER_TERMINAL_ARGUMENTS", `["literal;arg"]`)
	t.Setenv("MARTEN_COMMANDER_TERMINAL_SHELL_ARGUMENTS", `["--noprofile"]`)
	t.Setenv("MARTEN_COMMANDER_TERMINAL_HOLD", "false")
	c, err = LoadTerminalConfig()
	if err != nil || c.Hold || c.Arguments[0] != "literal;arg" || c.ShellArguments[0] != "--noprofile" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, test := range []struct{ key, value string }{{"MODE", "bad"}, {"ADAPTER", "bad"}, {"ARGUMENTS", "null"}, {"SHELL_ARGUMENTS", "{}"}, {"HOLD", "maybe"}, {"ARGUMENTS", `["\u0000"]`}} {
		t.Run(test.key+test.value, func(t *testing.T) {
			t.Setenv("MARTEN_COMMANDER_TERMINAL_"+test.key, test.value)
			if _, err := LoadTerminalConfig(); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
