//go:build !linux && !windows && !darwin

package localfs

import "fmt"

type SystemFileLauncher struct{}

func (SystemFileLauncher) OpenFile(string) error {
	return fmt.Errorf("системное открытие файлов на этой платформе не поддерживается")
}
