package volumes

import (
	"context"
	"fmt"
	"strings"
)

// OS-free drive enumeration logic is tested on every platform. The Windows
// implementation below this boundary calls Win32 directly, never a shell.
type driveAPI interface {
	drives() (uint32, error)
	driveType(string) uint32
	label(string) (string, error)
}

func driveRoots(mask uint32) []string {
	result := []string{}
	for i := 0; i < 26; i++ {
		if mask&(1<<i) != 0 {
			result = append(result, fmt.Sprintf("%c:\\", 'A'+i))
		}
	}
	return result
}

func windowsVolume(root string) Volume {
	return Volume{ID: "windows:" + strings.ToUpper(root), Name: root, Root: root}
}

func listDrives(ctx context.Context, api driveAPI) ([]Volume, error) {
	mask, err := api.drives()
	if err != nil {
		return nil, err
	}
	result := []Volume{}
	for _, root := range driveRoots(mask) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kind := api.driveType(root)
		if kind == 0 || kind == 1 {
			continue
		} // DRIVE_UNKNOWN / DRIVE_NO_ROOT_DIR
		volume := windowsVolume(root)
		if label, err := api.label(root); err == nil {
			if label != "" {
				volume.Name = label
			}
		} else {
			volume.Warning = "Метка тома недоступна: " + err.Error()
		}
		result = append(result, volume)
	}
	return result, nil
}
