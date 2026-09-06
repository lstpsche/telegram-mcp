//go:build darwin || linux

package privatefs

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func checkAncestors(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		var info unix.Stat_t
		err := unix.Lstat(parent, &info)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			if info.Mode&unix.S_IFMT == unix.S_IFLNK {
				if info.Uid != 0 && info.Uid != uint32(os.Geteuid()) {
					return errors.New("private path ancestor has an untrusted owner")
				}
				// macOS /tmp and /var are trusted OS aliases. Inspect the resolved chain too.
				resolved, err := filepath.EvalSymlinks(parent)
				if err != nil {
					return err
				}
				if err = checkResolvedAncestors(resolved); err != nil {
					return err
				}
			} else if err := checkAncestorStatus(&info); err != nil {
				return err
			}
		}
		if filepath.Dir(parent) == parent {
			return nil
		}
	}
}

func checkResolvedAncestors(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		var info unix.Stat_t
		if err := unix.Lstat(current, &info); err != nil {
			return err
		}
		if err := checkAncestorStatus(&info); err != nil {
			return err
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func checkAncestorStatus(info *unix.Stat_t) error {
	if info.Mode&unix.S_IFMT != unix.S_IFDIR || (info.Uid != 0 && info.Uid != uint32(os.Geteuid())) {
		return errors.New("private path ancestor has unsafe type or ownership")
	}
	if info.Mode&0022 != 0 && info.Mode&unix.S_ISVTX == 0 {
		return errors.New("private path ancestor is writable by another user")
	}
	return nil
}
