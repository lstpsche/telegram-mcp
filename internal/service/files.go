//go:build darwin

package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

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
	if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Uid != uint32(m.uid) || status.Mode&0o777 != 0o600 || status.Size > maxInstallationBytes {
		return nil, nil, ErrInvalidInstallation
	}
	data, err := io.ReadAll(io.LimitReader(file, maxInstallationBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxInstallationBytes {
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
	candidate := Config{BinDir: dir, Relay: filepath.Join(dir, "telegram-mcp"), Daemon: filepath.Join(dir, "telegram-mcpd")}
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
