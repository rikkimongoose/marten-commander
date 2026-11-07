//go:build linux

package localfs

import (
	"fmt"
	"os/exec"
	"time"
)

// SystemFileLauncher starts the desktop association handler without a shell.
// Waiting happens in a goroutine so the external application cannot block Wails.
type SystemFileLauncher struct{}

func (SystemFileLauncher) OpenFile(path string) error {
	cmd := exec.Command("xdg-open", path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("xdg-open: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("xdg-open: %w", err)
		}
		return nil
	case <-timer.C:
		// The association process is still running. Its waiter remains in the
		// goroutine, while the Wails command and UI can continue immediately.
		return nil
	}
}
