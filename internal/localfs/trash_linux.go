//go:build linux

package localfs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

// SystemTrash delegates storage, unique names and restore metadata to GIO.
// There is intentionally no permanent-delete capability in this adapter.
type SystemTrash struct{}

func (SystemTrash) InspectTrash(ctx context.Context, path string) (core.TrashSource, error) {
	if err := ctx.Err(); err != nil {
		return core.TrashSource{}, err
	}
	canonical, err := safeTrashPath(path)
	if err != nil {
		return core.TrashSource{}, err
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return core.TrashSource{}, err
	}
	token, _, ok := fingerprintTokens(info)
	if !ok {
		return core.TrashSource{}, fmt.Errorf("не удалось проверить идентичность %q", path)
	}
	if core.KindFromMode(info.Mode()) == core.KindOther {
		return core.TrashSource{}, fmt.Errorf("специальный объект нельзя переместить в корзину: %q", path)
	}
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical+"\x00"+token)))
	return core.TrashSource{Path: path, Name: filepath.Base(path), Kind: core.KindFromMode(info.Mode()), Revision: revision}, nil
}

func (SystemTrash) Trash(ctx context.Context, source core.TrashSource) error {
	return trashWith(ctx, source, exec.LookPath, func(exe string, args []string) ([]byte, error) { return exec.Command(exe, args...).CombinedOutput() })
}

func trashWith(ctx context.Context, source core.TrashSource, lookup func(string) (string, error), run func(string, []string) ([]byte, error)) error {
	exe, err := lookup("gio")
	if err != nil {
		return fmt.Errorf("системная корзина недоступна: gio не найден: %w", err)
	}
	current, err := (SystemTrash{}).InspectTrash(ctx, source.Path)
	if err != nil {
		return err
	}
	if source.Revision == "" || current.Revision != source.Revision {
		return fmt.Errorf("объект %q изменился после выбора: %w", source.Path, core.ErrSourceChanged)
	}
	canonical, err := safeTrashPath(source.Path)
	if err != nil {
		return err
	}
	// Do not kill an in-flight GIO process on batch cancellation: its actual exit
	// status must be observed before accounting for this item. Cancel stops the next item.
	output, err := run(exe, []string{"trash", "--", canonical})
	if err != nil {
		return fmt.Errorf("не удалось переместить %q в корзину: %w: %s", source.Path, err, strings.TrimSpace(string(output)))
	}
	if _, err = os.Lstat(canonical); !os.IsNotExist(err) {
		return fmt.Errorf("GIO завершился, но исчезновение источника %q не подтверждено", source.Path)
	}
	return nil
}

func withinTrashPath(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// Resolve parent links only; the final symlink must be trashed as a link.
func safeTrashPath(path string) (string, error) {
	deny := func() (string, error) {
		return "", fmt.Errorf("защищённый или некорректный путь: %q; корни томов и каталоги корзины перемещать нельзя", path)
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || filepath.Clean(path) != path || path == "/" {
		return deny()
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	canonical := filepath.Join(parent, filepath.Base(path))
	for _, part := range strings.Split(canonical, "/") {
		if part == ".Trash" || strings.HasPrefix(part, ".Trash-") {
			return deny()
		}
	}
	for _, root := range []string{"/proc", "/sys", "/dev"} {
		if withinTrashPath(canonical, root) {
			return deny()
		}
	}
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	trash := filepath.Join(data, "Trash")
	// Even when Trash does not exist yet, resolve existing ancestors so aliases
	// cannot bypass protection. Reject its ancestors as well as its contents.
	resolved, err := resolveMissingTrashPath(trash)
	if err != nil {
		return "", err
	}
	// Protect both the configured directory entry and its resolved storage. A
	// symlink used as XDG's Trash directory is infrastructure, not an ordinary link.
	configuredParent, err := resolveMissingTrashPath(filepath.Dir(trash))
	if err != nil {
		return "", err
	}
	for _, protected := range []string{filepath.Join(configuredParent, filepath.Base(trash)), resolved} {
		if withinTrashPath(canonical, protected) || withinTrashPath(protected, canonical) {
			return deny()
		}
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", fmt.Errorf("проверить корни томов: %w", err)
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mount := unescape.Replace(fields[4])
		if withinTrashPath(mount, canonical) {
			return deny()
		}
	}
	return canonical, nil
}

func resolveMissingTrashPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) || path == filepath.Dir(path) {
		return "", err
	}
	parent, err := resolveMissingTrashPath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}
