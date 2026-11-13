package localfs

import (
	"fmt"
	"os/exec"
	"time"
)

type SystemTerminalLauncher struct{}

// startTerminal waits only for immediate startup errors, never for the user's
// command. Wait always runs so long-lived children are eventually reaped.
func startTerminal(executable string, arguments []string, directory string) error {
	cmd := exec.Command(executable, arguments...)
	cmd.Dir = directory
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("терминал %s: %w", executable, err)
		}
	case <-timer.C:
	}
	return nil
}
