//go:build darwin

package localfs

import (
	"fmt"
	"os/exec"
)

type SystemFileLauncher struct{}

func (SystemFileLauncher) OpenFile(path string) error {
	cmd := exec.Command("open", path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
