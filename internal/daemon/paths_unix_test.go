//go:build darwin || linux

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

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
