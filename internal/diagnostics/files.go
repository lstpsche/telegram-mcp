package diagnostics

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"golang.org/x/sys/unix"
)

type FileCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func inspectFiles(paths daemon.Paths) ([]FileCheck, error) {
	expected, err := daemon.NewPaths(paths.StateDir, paths.RuntimeDir)
	if err != nil {
		return nil, err
	}
	if paths != expected {
		return nil, errors.New("diagnostic paths do not match runtime paths")
	}
	for _, directory := range []string{paths.StateDir, paths.RuntimeDir} {
		if err := inspectAncestors(directory); err != nil {
			return nil, err
		}
	}
	checks := []struct {
		name, path string
		mode       uint32
	}{
		{"state_directory", paths.StateDir, unix.S_IFDIR | 0o700},
		{"runtime_directory", paths.RuntimeDir, unix.S_IFDIR | 0o700},
		{"database", paths.Database, unix.S_IFREG | 0o600},
		{"account_lock", paths.Lock, unix.S_IFREG | 0o600},
		{"policy_lock", filepath.Join(paths.StateDir, "policy.lock"), unix.S_IFREG | 0o600},
	}
	result := make([]FileCheck, 0, len(checks))
	for _, check := range checks {
		var info unix.Stat_t
		err := unix.Lstat(check.path, &info)
		if errors.Is(err, os.ErrNotExist) {
			result = append(result, FileCheck{check.name, "absent"})
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Uid != uint32(os.Geteuid()) || uint32(info.Mode) != check.mode {
			return nil, errors.New("diagnostic file has unsafe ownership, type or permissions")
		}
		result = append(result, FileCheck{check.name, "present"})
	}
	return result, nil
}

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
