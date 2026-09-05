package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var (
	ErrInvalidInstallation = errors.New("service installation is not canonical")
	ErrUnsafePath          = errors.New("service path is unsafe")
	ErrSignature           = errors.New("binary signature verification failed")
)

const maxPlistBytes = 16 * 1024

func validatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || len(path) > 4096 || !utf8.ValidString(path) || strings.ContainsFunc(path, func(r rune) bool { return unicode.IsControl(r) || r == '\ufffe' || r == '\uffff' }) {
		return ErrUnsafePath
	}
	return nil
}

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
		if err := m.runner.Run(ctx, "/usr/bin/codesign", "--verify", "--strict", path); err != nil {
			return Config{}, errors.Join(ErrSignature, err)
		}
	}
	return config, nil
}

func escape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(value)
}

func (m *Manager) plist(config Config) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + Label + `</string>
  <key>ProgramArguments</key><array><string>` + escape(config.Daemon) + `</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
  <key>Umask</key><integer>63</integer>
  <key>ExitTimeOut</key><integer>20</integer>
  <key>EnvironmentVariables</key><dict>
    <key>HOME</key><string>` + escape(m.home) + `</string>
    <key>PATH</key><string>/usr/bin:/bin</string>
  </dict>
  <key>StandardOutPath</key><string>/dev/null</string>
  <key>StandardErrorPath</key><string>/dev/null</string>
</dict>
</plist>
`)
}

func (m *Manager) readInstallation(ctx context.Context) (config *Config, identity os.FileInfo, err error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := inspectAncestors(filepath.Dir(m.plistPath()), m.uid); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Inspect every existing ancestor before accepting an absent directory.
			for path := filepath.Dir(m.plistPath()); path != "/"; path = filepath.Dir(path) {
				if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
					continue
				} else if statErr != nil {
					return nil, nil, statErr
				}
				if err := inspectAncestors(path, m.uid); err != nil {
					return nil, nil, err
				}
				return nil, nil, nil
			}
		}
		return nil, nil, err
	}
	fd, err := unix.Open(m.plistPath(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, errors.Join(ErrInvalidInstallation, err)
	}
	file := os.NewFile(uintptr(fd), m.plistPath())
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			config = nil
			identity = nil
		}
	}()
	var status unix.Stat_t
	if err := unix.Fstat(fd, &status); err != nil {
		return nil, nil, err
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Uid != uint32(m.uid) || status.Mode&0o777 != 0o600 || status.Size > maxPlistBytes {
		return nil, nil, ErrInvalidInstallation
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPlistBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxPlistBytes {
		return nil, nil, ErrInvalidInstallation
	}
	var extracted struct {
		Arguments []string `xml:"dict>array>string"`
	}
	if err := xml.Unmarshal(data, &extracted); err != nil {
		return nil, nil, errors.Join(ErrInvalidInstallation, err)
	}
	if len(extracted.Arguments) != 1 || filepath.Base(extracted.Arguments[0]) != "telegram-mcpd" {
		return nil, nil, ErrInvalidInstallation
	}
	dir := filepath.Dir(extracted.Arguments[0])
	if err := validatePath(dir); err != nil {
		return nil, nil, errors.Join(ErrInvalidInstallation, err)
	}
	if err := validatePath(extracted.Arguments[0]); err != nil {
		return nil, nil, errors.Join(ErrInvalidInstallation, err)
	}
	candidate := Config{BinDir: dir, Relay: filepath.Join(dir, "telegram-mcp"), Daemon: filepath.Join(dir, "telegram-mcpd"), Control: filepath.Join(dir, "telegram-mcpctl")}
	if !bytes.Equal(data, m.plist(candidate)) {
		return nil, nil, ErrInvalidInstallation
	}
	identity, err = file.Stat()
	if err != nil {
		return nil, nil, err
	}
	current, err := os.Lstat(m.plistPath())
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(identity, current) {
		return nil, nil, ErrInvalidInstallation
	}
	return &candidate, identity, nil
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
