//go:build linux

package volumes

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const mountFixture = `1 0 8:1 / / rw - ext4 /dev/sda1 rw
2 1 0:1 / /proc rw - proc proc rw
3 1 0:2 / /sys rw - sysfs sysfs rw
4 1 0:3 / /run rw - tmpfs tmpfs rw
5 1 8:2 / /mnt/data rw shared:5 - ext4 /dev/sda2 rw
6 1 8:3 / /media/USB\040Drive rw - exfat /dev/sdb1 rw
7 1 0:4 / /mnt/network rw - nfs4 server:/share rw
8 1 0:5 / /var/lib/docker/overlay2/abc/merged rw - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/abc/diff
9 1 7:1 / /snap/app/123 ro - squashfs /dev/loop1 ro
10 1 8:4 / /var/lib/data rw - xfs /dev/sdc1 rw
11 1 8:5 / /run/media/user/disk rw - ext4 /dev/sdd1 rw
12 1 8:1 /var/lib/docker/containers/abc/resolv.conf /etc/resolv.conf rw - ext4 /dev/sda1 rw
13 1 8:1 /home/user/files /srv/bind rw - ext4 /dev/sda1 rw
14 1 0:6 / /mnt/ssh rw - fuse.sshfs user@host:/ rw
15 1 0:7 / /mnt/smb rw - cifs //server/share rw
16 1 8:6 / /var/lib/docker rw - ext4 /dev/sde1 rw
17 1 7:2 / /media/image ro - squashfs /dev/loop2 ro
18 1 0:8 / /mnt/user-overlay rw - overlay overlay rw,lowerdir=/home/user/base
19 1 0:9 / /tmp/image ro - fuse.App.AppImage App.AppImage ro,user_id=1000
20 1 8:1 /project /project rw - ext4 /dev/sda1 rw
21 1 8:1 /usr/share/hunspell /var/snap/browser/common/host-hunspell ro - ext4 /dev/sda1 rw
`

func TestMountEnumerationAndFiltering(t *testing.T) {
	mounts, err := readMounts(strings.NewReader(mountFixture))
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for _, v := range listedMounts(mounts) {
		roots = append(roots, v.Root)
	}
	want := []string{"/", "/media/USB Drive", "/media/image", "/mnt/data", "/mnt/network", "/mnt/smb", "/mnt/ssh", "/mnt/user-overlay", "/run/media/user/disk", "/srv/bind", "/var/lib/data", "/var/lib/docker"}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("roots: %v", roots)
	}
	if !visibleMount(mount{point: "/", fsType: "overlay", options: "lowerdir=/var/lib/docker/overlay2/base"}) {
		t.Fatal("container root omitted")
	}
	for _, fs := range []string{"devtmpfs", "cgroup2", "debugfs", "nsfs", "autofs", "fusectl", "tracefs", "bpf", "fuse.portal"} {
		if visibleMount(mount{point: "/anywhere", fsType: fs}) {
			t.Fatalf("technical fs included: %s", fs)
		}
	}
}

func TestContainingFilesystemAndOvermounts(t *testing.T) {
	mounts, _ := readMounts(strings.NewReader(mountFixture))
	for path, want := range map[string]string{"/home/user": "/", "/mnt/data/file": "/mnt/data", "/mnt/database": "/", "/mnt/network/subdir": "/mnt/network", "/proc/self": "/proc", "/srv/bind/file": "/srv/bind"} {
		v, err := containingMount(mounts, path, 0)
		if err != nil || v.Root != want {
			t.Fatalf("%s: %+v %v", path, v, err)
		}
	}
	mounts = append(mounts, mount{id: 30, point: "/mnt/data", root: "/", fsType: "ext4", source: "/dev/new"})
	v, err := containingMount(mounts, "/mnt/data/x", 30)
	if err != nil || v.ID != "linux:30" {
		t.Fatalf("actual mount id: %+v %v", v, err)
	}
	for _, v := range listedMounts(mounts) {
		if v.Root == "/mnt/data" && v.ID != "linux:30" {
			t.Fatal("covered mount included")
		}
	}
	if _, err := containingMount(mounts, "/mnt/data/x", 99); err == nil {
		t.Fatal("disconnected mount ID silently fell back")
	}
	if _, err := containingMount(nil, "/test", 0); err == nil {
		t.Fatal("missing filesystem accepted")
	}
}

func TestMountEscapingAndMalformedInput(t *testing.T) {
	value, err := unescapeMount(`/media/a\040b\011c\012d\134e`)
	if err != nil || value != "/media/a b\tc\nd\\e" {
		t.Fatalf("escape: %q %v", value, err)
	}
	for _, input := range []string{`1 0 1:1 /`, `a 0 1:1 / / rw - ext4 /dev/a rw`, `1 0 1:1 / relative rw - ext4 /dev/a rw`, `1 0 1:1 / /bad\xx rw - ext4 /dev/a rw`, `1 0 1:1 / /bad\000 rw - ext4 /dev/a rw`} {
		if _, err := readMounts(strings.NewReader(input)); err == nil {
			t.Fatalf("malformed mount accepted: %s", input)
		}
	}
}

func TestSystemMountsAndSymlinkLookup(t *testing.T) {
	service := NewService()
	items, err := service.ListVolumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundRoot := false
	for _, item := range items {
		if item.Root == "/" {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Fatal("root missing")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	v, err := service.GetVolumeInfo(context.Background(), link)
	if err != nil || v.Root != "/" {
		t.Fatalf("symlink lookup: %+v %v", v, err)
	}
	if v.Space != nil && (v.Space.Free > v.Space.Total || v.Space.Used != v.Space.Total-v.Space.Free) {
		t.Fatalf("invalid OS counts: %+v", v.Space)
	}
}

func TestLinuxSpaceUsesFragmentSizeAndSeparatesReservedSpace(t *testing.T) {
	stat := unix.Statfs_t{Blocks: 100, Bfree: 40, Bavail: 30, Bsize: 4096, Frsize: 1024}
	space, err := linuxSpace(stat)
	if err != nil || space != (Space{102400, 40960, 30720, 61440}) {
		t.Fatalf("fragment size: %+v %v", space, err)
	}
	stat.Frsize = 0
	space, err = linuxSpace(stat)
	if err != nil || space.Total != 409600 {
		t.Fatalf("block-size fallback: %+v %v", space, err)
	}
	stat.Bavail = ^uint64(0)
	space, err = linuxSpace(stat)
	if err != nil || space.Available != 0 {
		t.Fatalf("negative available: %+v %v", space, err)
	}
	stat.Bsize = -1
	if _, err := linuxSpace(stat); err == nil {
		t.Fatal("negative block size accepted")
	}
}

func TestNavigableMountExcludesFilesAndDisappearedMountpoints(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bound file")
	if err := os.WriteFile(file, []byte("a file bind mount is not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(dir, "disconnected")} {
		if _, keep := navigableMount(Volume{ID: "mount", Root: path}); keep {
			t.Fatalf("non-directory mount included: %q", path)
		}
	}
	volume, keep := navigableMount(Volume{ID: "directory", Root: dir})
	if !keep || volume.Root != dir || volume.Warning != "" {
		t.Fatalf("directory mount lost: %+v, %v", volume, keep)
	}
}

func TestNavigableMountRetainsPermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory traversal permissions")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "disk")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0700); err != nil {
			t.Error(err)
		}
	})
	volume, keep := navigableMount(Volume{ID: "restricted", Root: dir})
	if !keep || volume.Warning == "" || volume.Root != dir {
		t.Fatalf("inaccessible mount silently hidden: %+v, %v", volume, keep)
	}
}
