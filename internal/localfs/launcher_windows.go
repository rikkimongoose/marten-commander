//go:build windows

package localfs

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// SystemFileLauncher delegates to Windows file associations without creating a
// command line or treating the file name as executable input.
type SystemFileLauncher struct{}

func (SystemFileLauncher) OpenFile(path string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return fmt.Errorf("verb: %w", err)
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("path: %w", err)
	}
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("ShellExecute: %w", err)
	}
	return nil
}
