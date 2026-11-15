package app

import (
	"context"
	"github.com/rikkimongoose/marten-commander/internal/settings"
	"os"
	"path/filepath"
	"testing"
)

type terminalRecorder struct{ commands, directories []string }

func (r *terminalRecorder) LaunchTerminal(_ settings.TerminalConfig, command, directory string) error {
	r.commands = append(r.commands, command)
	r.directories = append(r.directories, directory)
	return nil
}
func TestTerminalServiceDirectoryAndValidation(t *testing.T) {
	r := &terminalRecorder{}
	s := NewTerminalService(r, settings.TerminalConfig{}, nil)
	before, _ := os.Getwd()
	dir := t.TempDir()
	if err := s.Run(context.Background(), "pwd", dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), "git status", dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Getwd()
	if before != after || r.directories[0] != dir || r.commands[1] != "git status" {
		t.Fatal(r)
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("x"), 0600)
	for _, p := range []string{"relative", filepath.Join(dir, "missing"), file, "sftp://host/path"} {
		if err := s.Run(context.Background(), "pwd", p); err == nil {
			t.Errorf("accepted %s", p)
		}
	}
	if err := s.Run(context.Background(), " \n", ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx, "pwd", dir); err == nil {
		t.Fatal("cancelled launch")
	}
	if len(r.commands) != 2 {
		t.Fatal(r)
	}
}
