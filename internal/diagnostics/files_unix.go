//go:build !windows

package diagnostics

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Missing child paths are conclusive only after checking the existing ancestry.
// Root-owned sticky temporary directories are safe parents for private fixtures.
func inspectAncestors(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		var info unix.Stat_t
		err := unix.Lstat(parent, &info)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			trustedOwner := info.Uid == 0 || info.Uid == uint32(os.Geteuid())
			stickyRoot := info.Uid == 0 && info.Mode&unix.S_ISVTX != 0
			if info.Mode&unix.S_IFMT != unix.S_IFDIR || !trustedOwner || info.Mode&0o022 != 0 && !stickyRoot {
				return errors.New("diagnostic directory ancestry is unsafe")
			}
		}
		if parent == string(filepath.Separator) {
			return nil
		}
	}
}
