//go:build !linux

package localfs

import (
	"fmt"
	"github.com/rikkimongoose/marten-commander/internal/settings"
)

func (SystemTerminalLauncher) LaunchTerminal(settings.TerminalConfig, string, string) error {
	return fmt.Errorf("внешний терминал пока поддерживается только в Linux")
}
