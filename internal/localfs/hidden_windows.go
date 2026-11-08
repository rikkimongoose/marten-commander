package localfs

import (
	"io/fs"
	"strings"
	"syscall"
)

func isHidden(info fs.FileInfo) bool {
	attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return strings.HasPrefix(info.Name(), ".") || (ok && attrs.FileAttributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0)
}
