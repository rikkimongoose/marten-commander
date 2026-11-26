package archive

import (
	"path"
	"strings"
)

// NormalizePath converts a relative or absolute archive path to a canonical,
// absolute POSIX path. Parent components, backslashes and NUL bytes are
// rejected rather than cleaned into a different, apparently safe path.
func NormalizePath(name string) (string, error) {
	if strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
		return "", pathError(name, ErrUnsafePath)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", pathError(name, ErrUnsafePath)
		}
	}
	if name == "" {
		return "/", nil
	}
	return path.Clean("/" + strings.TrimPrefix(name, "/")), nil
}
