//go:build linux

package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/operations"
	"golang.org/x/sys/unix"
)

func TestLocalExtractionDestinationRejectsDestinationSymlinks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		archive    string
		link       string
		linkTarget string
		targetDir  bool
	}{
		{"directory link outside", "linked/file", "linked", "outside", true},
		{"link to directory", "linked/file", "linked", "inside", true},
		{"link to file", "linked", "linked", "existing", false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			root := filepath.Join(base, "root")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(base, test.linkTarget)
			if test.targetDir {
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, test.link)); err != nil {
				t.Fatal(err)
			}
			size := int64(4)
			session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: test.archive, Kind: EntryFile, Size: &size}},
				contents: map[string][]byte{"/" + test.archive: []byte("data")}})
			defer session.Close()
			destination, err := NewLocalExtractionDestination(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer destination.Close()
			_, err = NewDefaultExtractor().Extract(context.Background(), session, destination,
				ExtractionConflictResolverFunc(func(context.Context, ExtractionConflict) (operations.ConflictAction, error) {
					return operations.Replace, nil
				}))
			if !errors.Is(err, ErrUnsafeDestination) {
				t.Fatalf("Extract error = %v", err)
			}
			if !test.targetDir {
				contents, readErr := os.ReadFile(target)
				if readErr != nil || string(contents) != "untouched" {
					t.Fatalf("symlink target changed: %q, %v", contents, readErr)
				}
			} else if _, statErr := os.Stat(filepath.Join(target, "file")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("file escaped through symlink: %v", statErr)
			}
		})
	}
}

func TestLocalExtractionDestinationRejectsUnsafeRoot(t *testing.T) {
	t.Parallel()
	if _, err := NewLocalExtractionDestination(context.Background(), "relative"); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("relative root error = %v", err)
	}
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	linkRoot := filepath.Join(base, "link")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalExtractionDestination(context.Background(), linkRoot); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("symlink root error = %v", err)
	}
}

func TestLocalExtractionPublishesUnicodeAndPreservesExistingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	destination, err := NewLocalExtractionDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()

	name := "Каталог/файл с пробелами.txt"
	session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: name, Kind: EntryFile}},
		contents: map[string][]byte{"/" + name: []byte("новые данные")}})
	defer session.Close()
	if _, err := NewDefaultExtractor().Extract(context.Background(), session, destination, nil); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil || string(contents) != "новые данные" {
		t.Fatalf("contents = %q, %v", contents, err)
	}

	existing := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(existing, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	conflictSession, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: "existing.txt", Kind: EntryFile}},
		contents: map[string][]byte{"/existing.txt": []byte("replacement")}})
	defer conflictSession.Close()
	if _, err := NewDefaultExtractor().Extract(context.Background(), conflictSession, destination, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	contents, err = os.ReadFile(existing)
	if err != nil || string(contents) != "original" {
		t.Fatalf("existing file changed: %q, %v", contents, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".marten-extract-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, err)
	}
}

func TestLocalExtractionExplicitReplaceAndCleanup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	existing := filepath.Join(root, "file")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination, err := NewLocalExtractionDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	session, _ := openFake(t, &fakeBackend{entries: []ArchiveEntry{{Path: "file", Kind: EntryFile}}, contents: map[string][]byte{"/file": []byte("new")}})
	defer session.Close()
	_, err = NewDefaultExtractor().Extract(context.Background(), session, destination,
		ExtractionConflictResolverFunc(func(context.Context, ExtractionConflict) (operations.ConflictAction, error) {
			return operations.Replace, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(existing)
	if err != nil || string(contents) != "new" {
		t.Fatalf("contents = %q, %v", contents, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".marten-extract-") {
		t.Fatalf("destination entries = %v, %v", entries, err)
	}
}

func TestLocalPublicationRejectsConcurrentDestinationChanges(t *testing.T) {
	t.Parallel()
	t.Run("new file appears", func(t *testing.T) {
		root := t.TempDir()
		destination, err := NewLocalExtractionDestination(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer destination.Close()
		pending := preparedLocalFile(t, destination, "file", "archive")
		if err := os.WriteFile(filepath.Join(root, "file"), []byte("concurrent"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := pending.PublishNew(context.Background()); !errors.Is(err, ErrConflict) {
			t.Fatalf("PublishNew error = %v", err)
		}
		if err := pending.Discard(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertFileContents(t, filepath.Join(root, "file"), "concurrent")
		assertNoLocalTemporaries(t, root)
	})

	t.Run("approved file replaced", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "file")
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination, err := NewLocalExtractionDestination(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer destination.Close()
		expected, err := destination.Inspect(context.Background(), "file")
		if err != nil {
			t.Fatal(err)
		}
		pending := preparedLocalFile(t, destination, "file", "archive")
		concurrent := filepath.Join(root, "concurrent")
		if err := os.WriteFile(concurrent, []byte("concurrent"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(concurrent, path); err != nil {
			t.Fatal(err)
		}
		if err := pending.PublishReplace(context.Background(), expected.Token, expected.Identity); !errors.Is(err, ErrConflict) {
			t.Fatalf("PublishReplace error = %v", err)
		}
		if err := pending.Discard(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertFileContents(t, path, "concurrent")
		assertNoLocalTemporaries(t, root)
	})

	t.Run("approved file becomes symlink", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "root")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "file")
		outside := filepath.Join(base, "outside")
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination, err := NewLocalExtractionDestination(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer destination.Close()
		expected, err := destination.Inspect(context.Background(), "file")
		if err != nil {
			t.Fatal(err)
		}
		pending := preparedLocalFile(t, destination, "file", "archive")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if err := pending.PublishReplace(context.Background(), expected.Token, expected.Identity); !errors.Is(err, ErrConflict) {
			t.Fatalf("PublishReplace error = %v", err)
		}
		if err := pending.Discard(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertFileContents(t, outside, "outside")
		if target, err := os.Readlink(path); err != nil || target != outside {
			t.Fatalf("destination symlink = %q, %v", target, err)
		}
		assertNoLocalTemporaries(t, root)
	})

	t.Run("post-exchange identity mismatch rolls back", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "file")
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination, err := NewLocalExtractionDestination(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer destination.Close()
		expected, err := destination.Inspect(context.Background(), "file")
		if err != nil {
			t.Fatal(err)
		}
		pending := preparedLocalFile(t, destination, "file", "archive")
		if err := pending.PublishReplace(context.Background(), expected.Token, "different-identity"); !errors.Is(err, ErrConflict) {
			t.Fatalf("PublishReplace error = %v", err)
		}
		if err := pending.Discard(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertFileContents(t, path, "original")
		assertNoLocalTemporaries(t, root)
	})
}

func TestLocalDestinationRejectsIntermediateSymlinkSwap(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	directory := filepath.Join(root, "directory")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	destination, err := NewLocalExtractionDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.EnsureDirectory(context.Background(), "directory"); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.CreateTemporary(context.Background(), "directory/file"); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("CreateTemporary error = %v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside entries = %v, %v", entries, err)
	}
}

func TestDestinationPathErrorClassifiesMountAndKernelSafetyFailures(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{unix.EXDEV, unix.ENOSYS, unix.ELOOP, unix.ENOTDIR} {
		if err := destinationPathError("open parent", "directory/file", cause); !errors.Is(err, ErrUnsafeDestination) || !errors.Is(err, cause) {
			t.Errorf("cause %v classified as %v", cause, err)
		}
	}
}

func TestLocalExtractionDestinationDoesNotLeakDescriptors(t *testing.T) {
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot inspect descriptors: %v", err)
	}
	root := t.TempDir()
	for index := 0; index < 50; index++ {
		destination, openErr := NewLocalExtractionDestination(context.Background(), root)
		if openErr != nil {
			t.Fatal(openErr)
		}
		directory := filepath.Join("nested", "level", "iteration-"+strconv.Itoa(index))
		if directoryErr := destination.EnsureDirectory(context.Background(), filepath.ToSlash(directory)); directoryErr != nil {
			t.Fatal(directoryErr)
		}
		file, createErr := destination.CreateTemporary(context.Background(), filepath.ToSlash(filepath.Join(directory, "file")))
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := file.Write([]byte("data")); writeErr != nil {
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if discardErr := file.Discard(context.Background()); discardErr != nil {
			t.Fatal(discardErr)
		}
		if closeErr := destination.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before)+1 {
		t.Fatalf("file descriptors grew from %d to %d", len(before), len(after))
	}
}

func preparedLocalFile(t *testing.T, destination *LocalExtractionDestination, name, contents string) ExtractionFile {
	t.Helper()
	file, err := destination.CreateTemporary(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func assertFileContents(t *testing.T, name, expected string) {
	t.Helper()
	contents, err := os.ReadFile(name)
	if err != nil || string(contents) != expected {
		t.Fatalf("contents of %q = %q, %v; want %q", name, contents, err, expected)
	}
}

func assertNoLocalTemporaries(t *testing.T, root string) {
	t.Helper()
	var matches []string
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasPrefix(entry.Name(), ".marten-extract-") {
			matches = append(matches, name)
		}
		return nil
	})
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, err)
	}
}
