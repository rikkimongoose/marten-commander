//go:build windows

package localfs

import "github.com/rikkimongoose/marten-commander/internal/settings"

func (SystemEditorLauncher) LaunchEditor(config settings.EditorConfig, path string) error {
	return launchEditor(config, path, []editorCandidate{{executable: "notepad.exe"}})
}
