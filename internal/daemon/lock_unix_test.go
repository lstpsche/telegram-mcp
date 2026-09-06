//go:build darwin || linux

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccountLockRejectsUnsafeNodes(t *testing.T) {
	t.Parallel()

	t.Run("symlink", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(stateDir, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(stateDir, lockFilename)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if lock, err := AcquireAccountLock(link); err == nil {
			_ = lock.Release()
			t.Fatal("AcquireAccountLock() accepted a symlink")
		}
	})

	t.Run("wrong permissions", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(stateDir, lockFilename)
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lockPath, 0o640); err != nil {
			t.Fatal(err)
		}
		if lock, err := AcquireAccountLock(lockPath); err == nil {
			_ = lock.Release()
			t.Fatal("AcquireAccountLock() accepted unsafe permissions")
		}
	})
}
