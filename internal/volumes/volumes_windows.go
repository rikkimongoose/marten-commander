//go:build windows

package volumes

import (
	"context"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type systemBackend struct{}

var setThreadErrorMode = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadErrorMode")

// An empty card reader must return an error, not open a native "insert disk"
// dialog. Scope the error mode to this locked thread and restore it afterwards.
func mediaCall(call func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var previous uint32
	ok, _, err := setThreadErrorMode.Call(windows.SEM_FAILCRITICALERRORS, uintptr(unsafe.Pointer(&previous)))
	if ok == 0 {
		return err
	}
	defer setThreadErrorMode.Call(uintptr(previous), 0)
	return call()
}

type win32Drives struct{}

func (win32Drives) drives() (uint32, error) { return windows.GetLogicalDrives() }
func (win32Drives) driveType(root string) uint32 {
	pointer, _ := windows.UTF16PtrFromString(root)
	return windows.GetDriveType(pointer)
}
func (win32Drives) label(root string) (string, error) {
	pointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", err
	}
	var label [261]uint16
	err = mediaCall(func() error {
		return windows.GetVolumeInformation(pointer, &label[0], uint32(len(label)), nil, nil, nil, nil, 0)
	})
	return windows.UTF16ToString(label[:]), err
}

func (systemBackend) list(ctx context.Context) ([]Volume, error) {
	return listDrives(ctx, win32Drives{})
}

func (systemBackend) locate(ctx context.Context, path string) (Volume, error) {
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Volume{}, err
	}
	var root [32768]uint16
	if err := windows.GetVolumePathName(pointer, &root[0], uint32(len(root))); err != nil {
		return Volume{}, err
	}
	volume := windowsVolume(windows.UTF16ToString(root[:]))
	if value, err := (win32Drives{}).label(volume.Root); err == nil && value != "" {
		volume.Name = value
	}
	return volume, nil
}

func (systemBackend) space(path string) (Space, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Space{}, err
	}
	var available, total uint64
	if err := mediaCall(func() error { return windows.GetDiskFreeSpaceEx(pointer, &available, &total, nil) }); err != nil {
		return Space{}, err
	}
	// Both counters refer to the caller's quota, not a mixture of quota-aware
	// total and physical disk free space. Used means occupied quota on Windows.
	return spaceFromCounts(total, available, available, 1)
}
