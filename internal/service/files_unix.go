//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// inspectAncestors rejects symlinks and directories writable by another user.
// Root-owned sticky temporary directories are safe parents of owned directories.
func inspectAncestors(path string, uid int) error {
	if err := validatePath(path); err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		var status unix.Stat_t
		if err := unix.Lstat(current, &status); err != nil {
			return fmt.Errorf("inspect service directory: %w", err)
		}
		stickyRoot := status.Uid == 0 && status.Mode&unix.S_ISVTX != 0
		if status.Mode&unix.S_IFMT != unix.S_IFDIR || (status.Uid != 0 && status.Uid != uint32(uid)) || (status.Mode&0o022 != 0 && !stickyRoot) {
			return ErrUnsafePath
		}
		if current == "/" {
			return nil
		}
	}
}

func inspectDirectory(path string, uid int, private bool) error {
	if err := inspectAncestors(path, uid); err != nil {
		return err
	}
	var status unix.Stat_t
	if err := unix.Lstat(path, &status); err != nil {
		return fmt.Errorf("inspect owned directory: %w", err)
	}
	if status.Uid != uint32(uid) || (private && status.Mode&0o777 != 0o700) {
		return ErrUnsafePath
	}
	return nil
}

func ensureDirectory(path string, uid int, private bool) error {
	if err := validatePath(path); err != nil {
		return err
	}
	if err := inspectDirectory(path, uid, private); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := inspectAncestors(parent, uid); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := ensureDirectory(parent, uid, false); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create service directory: %w", err)
	}
	return inspectDirectory(path, uid, private)
}

func (m *Manager) inspectBinaries(ctx context.Context, dir string) (Config, error) {
	if err := inspectDirectory(dir, m.uid, true); err != nil {
		return Config{}, err
	}
	config := Config{BinDir: dir, Relay: filepath.Join(dir, "telegram-mcp"), Daemon: filepath.Join(dir, "telegram-mcpd"), Control: filepath.Join(dir, "telegram-mcpctl")}
	for _, path := range []string{config.Relay, config.Daemon, config.Control} {
		if err := validatePath(path); err != nil {
			return Config{}, err
		}
		var status unix.Stat_t
		if err := unix.Lstat(path, &status); err != nil {
			return Config{}, fmt.Errorf("inspect service binary: %w", err)
		}
		if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Uid != uint32(m.uid) || status.Mode&0o022 != 0 || status.Mode&0o100 == 0 || status.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 {
			return Config{}, ErrUnsafePath
		}
	}
	return config, nil
}

func writeExclusive(path string, data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".telegram-mcp-")
	if err != nil {
		return fmt.Errorf("create installation file: %w", err)
	}
	defer func() { err = errors.Join(err, os.Remove(file.Name())) }()
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil {
		return fmt.Errorf("publish installation file: %w", err)
	}
	return nil
}
