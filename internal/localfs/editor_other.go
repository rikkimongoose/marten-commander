//go:build !linux && !windows && !darwin

package localfs

import "github.com/rikkimongoose/marten-commander/internal/settings"

func (SystemEditorLauncher) LaunchEditor(settings.EditorConfig, string) error {
	return settings.ErrEditorNotFound
}
