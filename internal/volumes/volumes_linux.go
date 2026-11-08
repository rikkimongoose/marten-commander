//go:build linux

package volumes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type systemBackend struct{}

type mount struct {
	id                                   uint64
	root, point, fsType, source, options string
}

func unescapeMount(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			out.WriteByte(value[i])
			continue
		}
		if i+3 >= len(value) {
			return "", fmt.Errorf("неполная escape-последовательность mountinfo")
		}
		n, err := strconv.ParseUint(value[i+1:i+4], 8, 8)
		if err != nil || n == 0 {
			return "", fmt.Errorf("некорректная escape-последовательность mountinfo")
		}
		out.WriteByte(byte(n))
		i += 3
	}
	return out.String(), nil
}

func readMounts(reader io.Reader) ([]mount, error) {
	result := []mount{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || len(fields) < separator+4 {
			return nil, fmt.Errorf("некорректная строка mountinfo")
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, err
		}
		root, err := unescapeMount(fields[3])
		if err != nil {
			return nil, err
		}
		point, err := unescapeMount(fields[4])
		if err != nil {
			return nil, err
		}
		source, err := unescapeMount(fields[separator+2])
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(point) {
			return nil, fmt.Errorf("неабсолютная точка монтирования")
		}
		result = append(result, mount{id, root, point, fields[separator+1], source, fields[separator+3]})
	}
	return result, scanner.Err()
}

func systemMounts() ([]mount, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readMounts(file)
}

func isWithin(path, root string) bool {
	return path == root || root == "/" || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

func visibleMount(m mount) bool {
	if m.point == "/" {
		return true
	}
	switch m.fsType {
	case "proc", "sysfs", "devtmpfs", "devpts", "tmpfs", "ramfs", "cgroup", "cgroup2", "securityfs", "debugfs", "tracefs",
		"configfs", "pstore", "efivarfs", "mqueue", "hugetlbfs", "fusectl", "binfmt_misc", "autofs", "nsfs", "rpc_pipefs", "bpf", "selinuxfs", "fuse.portal":
		return false
	}
	// An overlay is not inherently technical: keep user overlays. Container
	// overlays have backing directories owned by the container storage driver.
	containerStorage := func(path string) bool {
		return strings.Contains(path, "/docker/overlay2/") || strings.Contains(path, "/docker/containers/") ||
			strings.Contains(path, "/containerd/io.containerd.snapshotter.") || strings.Contains(path, "/containers/storage/overlay")
	}
	if (m.fsType == "overlay" || m.fsType == "fuse.overlayfs") && containerStorage(m.options) {
		return false
	}
	// Bind-mounted container internals (including resolv.conf/hostname files).
	if m.root != "/" && containerStorage(m.root) &&
		(m.fsType == "ext4" || m.fsType == "xfs" || m.fsType == "btrfs" || m.fsType == "overlay") {
		return false
	}
	if m.fsType == "squashfs" && strings.HasPrefix(m.source, "/dev/loop") &&
		(isWithin(m.point, "/snap") || isWithin(m.point, "/var/lib/snapd/snap")) {
		return false
	}
	if strings.HasPrefix(m.fsType, "fuse.") && strings.HasSuffix(m.source, ".AppImage") &&
		strings.Contains(","+m.options+",", ",ro,") {
		return false
	}
	if (m.fsType == "ext4" || m.fsType == "xfs" || m.fsType == "btrfs") &&
		m.root == "/usr/share/hunspell" && isWithin(m.point, "/var/snap") && filepath.Base(m.point) == "host-hunspell" {
		return false
	}
	return true
}

func (m mount) volume() Volume {
	name := m.source
	if m.point == "/" {
		name = "System"
	}
	if name == "" || name == "none" {
		name = m.fsType
	}
	return Volume{ID: fmt.Sprintf("linux:%d", m.id), Name: name, Root: m.point}
}

func listedMounts(mounts []mount) []Volume {
	// Keep the last entry at a mountpoint, not a covered filesystem beneath it.
	points := map[string]mount{}
	for _, m := range mounts {
		points[m.point] = m
	}
	result := []Volume{}
	for _, m := range points {
		base := points["/"]
		// Permission-only self-binds in sandboxes do not expose a new location.
		if m.point != "/" && m.root == m.point && m.source == base.source && m.fsType == base.fsType {
			continue
		}
		if visibleMount(m) {
			result = append(result, m.volume())
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Root < result[j].Root })
	return result
}

func containingMount(mounts []mount, path string, mountID uint64) (Volume, error) {
	if mountID != 0 {
		for _, m := range mounts {
			if m.id == mountID {
				return m.volume(), nil
			}
		}
		return Volume{}, fmt.Errorf("том отключён или список точек монтирования изменился")
	}
	var found *mount
	for i := range mounts {
		m := &mounts[i]
		if isWithin(path, m.point) && (found == nil || len(m.point) >= len(found.point)) {
			found = m
		}
	}
	if found == nil {
		return Volume{}, fmt.Errorf("файловая система для пути не найдена")
	}
	return found.volume(), nil
}

func (systemBackend) list(ctx context.Context) ([]Volume, error) {
	mounts, err := systemMounts()
	if err != nil {
		return nil, err
	}
	result := []Volume{}
	for _, volume := range listedMounts(mounts) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if volume, keep := navigableMount(volume); keep {
			result = append(result, volume)
		}
	}
	return result, ctx.Err()
}

// mountinfo also includes file bind mounts, which cannot be opened as panels.
// O_PATH checks the mountpoint type without reading directory contents or space.
// Keep permission failures visible rather than silently hiding a user's disk.
func navigableMount(volume Volume) (Volume, bool) {
	fd, err := unix.Open(volume.Root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ENOENT) {
		return Volume{}, false
	}
	if err == nil {
		err = unix.Close(fd)
	}
	if err != nil {
		volume.Warning = "Точка монтирования недоступна: " + err.Error()
	}
	return volume, true
}

func (systemBackend) locate(ctx context.Context, path string) (Volume, error) {
	mounts, err := systemMounts()
	if err != nil {
		return Volume{}, err
	}
	if err := ctx.Err(); err != nil {
		return Volume{}, err
	}
	// statx identifies the actual mount, including bind mounts and overmounts.
	// Older kernels without STATX_MNT_ID use component-aware longest-prefix lookup.
	var stat unix.Statx_t
	var id uint64
	if unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_MNT_ID, &stat) == nil && stat.Mask&unix.STATX_MNT_ID != 0 {
		id = stat.Mnt_id
	}
	return containingMount(mounts, path, id)
}

func (systemBackend) space(path string) (Space, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Space{}, err
	}
	return linuxSpace(stat)
}

func linuxSpace(stat unix.Statfs_t) (Space, error) {
	blockSize := stat.Frsize
	if blockSize == 0 {
		blockSize = stat.Bsize
	}
	if blockSize <= 0 {
		return Space{}, fmt.Errorf("некорректный размер блока")
	}
	// Some filesystems report a negative signed f_bavail through the uint64 ABI.
	available := stat.Bavail
	if int64(available) < 0 {
		available = 0
	}
	return spaceFromCounts(stat.Blocks, stat.Bfree, available, uint64(blockSize))
}
