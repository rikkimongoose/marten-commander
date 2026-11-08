//go:build linux

package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func expandHomePath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("не удалось определить домашний каталог: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("не удалось определить домашний каталог")
	}
	if path == "~" || path == "~/" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
