//go:build !linux

package desktop

func expandHomePath(path string) (string, error) { return path, nil }
