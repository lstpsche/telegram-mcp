//go:build darwin

// Package service manages the current user's fixed launchd registration.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

type Manager struct {
	home   string
	uid    int
	runner Runner
}

func New(home string, uid int, runner Runner) (*Manager, error) {
	if uid <= 0 || uid != os.Geteuid() || runner == nil {
		return nil, errors.New("service requires the current non-root user and a process runner")
	}
	if err := validatePath(home); err != nil {
		return nil, err
	}
	if err := inspectDirectory(home, uid, false); err != nil {
		return nil, fmt.Errorf("inspect service home: %w", err)
	}
	return &Manager{home: home, uid: uid, runner: runner}, nil
}

func Default() (*Manager, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("user service management requires macOS")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve service home: %w", err)
	}
	return New(home, os.Geteuid(), processRunner{})
}

func (m *Manager) domain() string { return "gui/" + strconv.Itoa(m.uid) }

func (m *Manager) target() string { return m.domain() + "/" + Label }

func (m *Manager) plistPath() string {
	return filepath.Join(m.home, "Library", "LaunchAgents", Label+".plist")
}

func (m *Manager) install(ctx context.Context, binDir string, enabled bool) (config Config, err error) {
	if _, err := os.Lstat(m.plistPath()); err == nil {
		return Config{}, ErrAlreadyInstalled
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("inspect installation path: %w", err)
	}
	config, err = m.inspectBinaries(ctx, binDir)
	if err != nil {
		return Config{}, err
	}
	plist := m.plist(config)
	if len(plist) > maxInstallationBytes {
		return Config{}, ErrUnsafePath
	}
	loaded, err := m.loaded(ctx)
	if err != nil {
		return Config{}, err
	}
	if loaded {
		return Config{}, ErrServiceLoaded
	}
	if err := ensureDirectory(filepath.Dir(m.plistPath()), m.uid, false); err != nil {
		return Config{}, err
	}
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	if !enabled {
		if err := m.setEnabled(ctx, false); err != nil {
			return Config{}, err
		}
	}
	if err := writeExclusive(m.plistPath(), plist); err != nil {
		return Config{}, err
	}
	if enabled {
		if err := m.setEnabled(ctx, true); err != nil {
			return Config{}, err
		}
	}
	return config, nil
}

// Inspect performs no filesystem writes. A nil config means a proven absent plist.
func (m *Manager) Inspect(ctx context.Context) (*Config, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	config, _, err := m.readInstallation(ctx)
	if err != nil || config == nil {
		return nil, err
	}
	verified, err := m.inspectBinaries(ctx, config.BinDir)
	if err != nil {
		return nil, err
	}
	return &verified, nil
}

func (m *Manager) requireRecord(ctx context.Context) error {
	config, _, err := m.readInstallation(ctx)
	if err != nil {
		return err
	}
	if config == nil {
		return ErrNotInstalled
	}
	return nil
}

func (m *Manager) stop(ctx context.Context) error {
	if err := m.requireRecord(ctx); err != nil {
		return err
	}
	loaded, err := m.loaded(ctx)
	if err != nil {
		return err
	}
	if !loaded {
		return ErrServiceAbsent
	}
	if err := m.runner.Run(ctx, "/bin/launchctl", "bootout", m.target()); err != nil {
		return fmt.Errorf("unregister service: %w", err)
	}
	for {
		loaded, err := m.loaded(ctx)
		if err != nil {
			return err
		}
		if !loaded {
			return nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) uninstall(ctx context.Context) error {
	config, identity, err := m.readInstallation(ctx)
	if err != nil {
		return err
	}
	if config == nil {
		return ErrNotInstalled
	}
	loaded, err := m.loaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		return ErrServiceLoaded
	}
	if err := m.waitAccountRelease(ctx); err != nil {
		return err
	}
	current, err := os.Lstat(m.plistPath())
	if err != nil {
		return fmt.Errorf("inspect installation before removal: %w", err)
	}
	if !os.SameFile(identity, current) {
		return errors.New("installation changed before removal")
	}
	if err := os.Remove(m.plistPath()); err != nil {
		return fmt.Errorf("remove service installation: %w", err)
	}
	return nil
}

func (m *Manager) stateDir() string {
	return filepath.Join(m.home, "Library", "Application Support", "Telegram MCP")
}
func (m *Manager) accountPath() string { return filepath.Join(m.stateDir(), "account.lock") }
