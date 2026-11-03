//go:build !windows

package localfs

import (
	"io/fs"
	"strings"
)

func isHidden(info fs.FileInfo) bool { return strings.HasPrefix(info.Name(), ".") }
