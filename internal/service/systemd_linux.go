package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func validateUser(home string, uid int) error {
	if uid <= 0 || uid != os.Geteuid() {
		return errors.New("service requires the current non-root user")
	}
	return inspectDirectory(home, uid, false)
}

func (m *Manager) stateDir() string {
	if m.storageDir != "" {
		return m.storageDir
	}
	return filepath.Join(m.home, ".config", "Telegram MCP")
}

func (m *Manager) unitPath() string {
	return filepath.Join(filepath.Dir(m.stateDir()), "systemd", "user", Label+".service")
}

func binaryConfig(dir string) Config {
	return Config{dir, filepath.Join(dir, "telegram-mcp"), filepath.Join(dir, "telegram-mcpd"), filepath.Join(dir, "telegram-mcpctl")}
}

func (m *Manager) unit(config Config) []byte {
	// systemd expands percent specifiers even inside quoted command arguments.
	command := strconv.Quote(strings.ReplaceAll(config.Daemon, "%", "%%"))
	environment := "Environment=" + strconv.Quote("XDG_CONFIG_HOME="+strings.ReplaceAll(filepath.Dir(m.stateDir()), "%", "%%")) + "\nEnvironment=" + strconv.Quote("XDG_CACHE_HOME="+strings.ReplaceAll(m.cacheRoot, "%", "%%")) + "\n"
	return []byte("[Unit]\nDescription=Telegram MCP\n\n[Service]\nType=simple\nExecStart=:" + command + "\n" + environment + "UMask=0077\nTimeoutStopSec=20\nStandardOutput=null\nStandardError=null\n\n[Install]\nWantedBy=default.target\n")
}

func (m *Manager) recordPath() string { return m.unitPath() }

func (m *Manager) readRegistration(ctx context.Context) (Config, error) {
	if err := inspectAncestors(filepath.Dir(m.unitPath()), m.uid); err != nil {
		return Config{}, err
	}
	data, err := readUnit(m.unitPath())
	if err != nil {
		return Config{}, err
	}
	var command string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			command = strings.TrimPrefix(line, "ExecStart=")
			break
		}
	}
	executable, err := strconv.Unquote(strings.TrimPrefix(command, ":"))
	if err != nil {
		return Config{}, ErrInvalidInstallation
	}
	executable = strings.ReplaceAll(executable, "%%", "%")
	config := binaryConfig(filepath.Dir(executable))
	if !bytes.Equal(data, m.unit(config)) {
		return Config{}, ErrInvalidInstallation
	}
	return config, ctx.Err()
}

func (m *Manager) installRegistration(ctx context.Context, config Config) error {
	if err := ensureDirectory(filepath.Dir(m.unitPath()), m.uid, false); err != nil {
		return err
	}
	if err := writeExclusive(m.unitPath(), m.unit(config)); err != nil {
		return err
	}
	return m.activateRegistration(ctx)
}

func (m *Manager) activateRegistration(ctx context.Context) error {
	if err := m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "enable", Label+".service")
}

func (m *Manager) running(ctx context.Context) (bool, error) {
	err := m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "is-active", "--quiet", Label+".service")
	if err == nil {
		return true, nil
	}
	if ctx.Err() == nil && exited(err, 3) {
		return false, nil
	}
	return false, err
}

func (m *Manager) startRegistration(ctx context.Context) error {
	return m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "start", Label+".service")
}

func (m *Manager) stopRegistration(ctx context.Context) error {
	return m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "stop", Label+".service")
}

func (m *Manager) removeRegistration(ctx context.Context) error {
	if err := m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "disable", Label+".service"); err != nil {
		return err
	}
	if err := os.Remove(m.unitPath()); err != nil {
		return err
	}
	return m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "daemon-reload")
}

func defaultAccountLock(home string) string {
	return filepath.Join(home, ".config", "Telegram MCP", "account.lock")
}

func (m *Manager) registrationExists(ctx context.Context) (bool, error) {
	err := m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "is-enabled", "--quiet", Label+".service")
	if err == nil {
		return true, nil
	}
	if ctx.Err() == nil && exited(err, 1) {
		return false, nil
	}
	return false, err
}
