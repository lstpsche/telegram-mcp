package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewPathsRequiresBoundedAbsoluteRoots(t *testing.T) {
	t.Parallel()

	if _, err := NewPaths("relative", t.TempDir()); err == nil {
		t.Fatal("NewPaths() accepted a relative state directory")
	}
	if _, err := NewPaths(string(filepath.Separator), t.TempDir()); err == nil {
		t.Fatal("NewPaths() accepted the filesystem root")
	}
	paths, err := NewPaths(filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(paths.Database) != databaseFilename || filepath.Base(paths.Socket) != socketFilename {
		t.Fatalf("NewPaths() = %#v", paths)
	}
}

func TestEnsurePrivateDirectoryFailsClosed(t *testing.T) {
	t.Parallel()

	t.Run("wrong permissions", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "runtime")
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := ensurePrivateDirectory(directory); err == nil {
			t.Fatal("ensurePrivateDirectory() accepted group/other permissions")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := ensurePrivateDirectory(link); err == nil {
			t.Fatal("ensurePrivateDirectory() accepted a symlink")
		}
	})
}
