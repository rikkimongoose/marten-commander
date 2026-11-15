package settings

import (
	"errors"
	"reflect"
	"testing"
)

func TestLoadEditorConfigDefaultsToAuto(t *testing.T) {
	t.Setenv("MARTEN_COMMANDER_EDITOR_MODE", "")
	t.Setenv("MARTEN_COMMANDER_EDITOR_EXECUTABLE", "")
	t.Setenv("MARTEN_COMMANDER_EDITOR_ARGUMENTS", "")
	config, err := LoadEditorConfig()
	if err != nil || config.Mode != EditorAuto {
		t.Fatalf("config=%#v err=%v", config, err)
	}
}

func TestLoadCustomEditorConfigPreservesTokenisedArguments(t *testing.T) {
	t.Setenv("MARTEN_COMMANDER_EDITOR_MODE", "custom")
	t.Setenv("MARTEN_COMMANDER_EDITOR_EXECUTABLE", "/opt/My Editor/editor")
	t.Setenv("MARTEN_COMMANDER_EDITOR_ARGUMENTS", `["--new-window","{file}","literal;not-shell"]`)
	config, err := LoadEditorConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Executable != "/opt/My Editor/editor" || !reflect.DeepEqual(config.Arguments, []string{"--new-window", "{file}", "literal;not-shell"}) {
		t.Fatalf("%#v", config)
	}
}

func TestEditorConfigRejectsInvalidModeJSONAndUnsafeTemplate(t *testing.T) {
	for _, test := range []struct{ mode, executable, arguments string }{
		{"other", "", ""}, {"custom", "", ""}, {"custom", "editor", `not-json`},
		{"custom", "editor", `["--file={file}"]`}, {"custom", "editor", `["{file}","{file}"]`},
	} {
		t.Setenv("MARTEN_COMMANDER_EDITOR_MODE", test.mode)
		t.Setenv("MARTEN_COMMANDER_EDITOR_EXECUTABLE", test.executable)
		t.Setenv("MARTEN_COMMANDER_EDITOR_ARGUMENTS", test.arguments)
		if _, err := LoadEditorConfig(); !errors.Is(err, ErrInvalidEditorConfig) {
			t.Errorf("%+v: %v", test, err)
		}
	}
}
