//go:build linux

package localfs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/settings"
)

func writeEditor(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAutoEditorFindsSupportedGUIEditorAndPassesLiteralPath(t *testing.T) {
	directory := t.TempDir()
	capture := filepath.Join(directory, "capture")
	writeEditor(t, directory, "gedit", "printf '%s' \"$1\" > \"$CAPTURE\"\n")
	t.Setenv("PATH", directory)
	t.Setenv("CAPTURE", capture)
	path := filepath.Join(directory, "файл с пробелами.txt")
	if err := (localfs.SystemEditorLauncher{}).LaunchEditor(settings.EditorConfig{Mode: settings.EditorAuto}, path); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(capture); err != nil || string(data) != path {
		t.Fatalf("argument=%q err=%v", data, err)
	}
}

func TestCustomEditorUsesTokenisedArgumentsWithoutShell(t *testing.T) {
	directory := t.TempDir()
	capture, injected := filepath.Join(directory, "capture"), filepath.Join(directory, "injected")
	executable := writeEditor(t, directory, "custom editor", "printf '%s\n' \"$@\" > \"$CAPTURE\"\n")
	t.Setenv("CAPTURE", capture)
	path := filepath.Join(directory, "Кириллица с пробелами.txt")
	literal := "$(touch " + injected + ")"
	config := settings.EditorConfig{Mode: settings.EditorCustom, Executable: executable, Arguments: []string{"--new-window", literal, "{file}"}}
	if err := (localfs.SystemEditorLauncher{}).LaunchEditor(config, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSpace(string(data)), "\n"); len(got) != 3 || got[0] != "--new-window" || got[1] != literal || got[2] != path {
		t.Fatalf("arguments: %#v", got)
	}
	if _, err := os.Stat(injected); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("shell substitution executed")
	}
}

func TestEditorReportsMissingAutoAndCustomExecutable(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	launcher := localfs.SystemEditorLauncher{}
	if err := launcher.LaunchEditor(settings.EditorConfig{Mode: settings.EditorAuto}, "/tmp/file"); !errors.Is(err, settings.ErrEditorNotFound) {
		t.Fatalf("auto: %v", err)
	}
	if err := launcher.LaunchEditor(settings.EditorConfig{Mode: settings.EditorCustom, Executable: "missing"}, "/tmp/file"); !errors.Is(err, settings.ErrEditorNotFound) {
		t.Fatalf("custom: %v", err)
	}
	if err := launcher.LaunchEditor(settings.EditorConfig{Mode: settings.EditorCustom, Executable: "x", Arguments: []string{"--file={file}"}}, "/tmp/file"); !errors.Is(err, settings.ErrInvalidEditorConfig) {
		t.Fatalf("template: %v", err)
	}
}

func TestEditorReportsImmediateProcessFailure(t *testing.T) {
	directory := t.TempDir()
	executable := writeEditor(t, directory, "broken-editor", "exit 23\n")
	err := (localfs.SystemEditorLauncher{}).LaunchEditor(settings.EditorConfig{
		Mode: settings.EditorCustom, Executable: executable,
	}, filepath.Join(directory, "file.txt"))
	if err == nil {
		t.Fatal("immediate editor failure was ignored")
	}
}
