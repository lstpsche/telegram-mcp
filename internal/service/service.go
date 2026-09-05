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

	"github.com/lstpsche/telegram-mcp/internal/daemon"
)

const Label = "dev.telegram-mcp.gateway"

var (
	ErrNotInstalled     = errors.New("service is not installed")
	ErrAlreadyInstalled = errors.New("service is already installed")
	ErrServiceLoaded    = errors.New("service is already registered")
	ErrServiceAbsent    = errors.New("service is not registered")
)

// Config records the exact binaries selected by the installed plist.
type Config struct {
	BinDir  string
	Relay   string
	Daemon  string
	Control string
}

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

func (m *Manager) lock() (*daemon.AccountLock, error) {
	dir := filepath.Join(m.home, "Library", "Application Support", "Telegram MCP")
	if err := ensureDirectory(dir, m.uid, true); err != nil {
		return nil, err
	}
	return daemon.AcquireAccountLock(filepath.Join(dir, "service.lock"))
}

func (m *Manager) Install(ctx context.Context, binDir string) (config Config, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	lock, err := m.lock()
	if err != nil {
		return Config{}, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
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
	if len(plist) > maxPlistBytes {
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
	if err := writeExclusive(m.plistPath(), plist); err != nil {
		return Config{}, err
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

func (m *Manager) mutate(ctx context.Context, action func(context.Context) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := m.lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	return action(ctx)
}

func (m *Manager) requireInstallation(ctx context.Context) error {
	config, _, err := m.readInstallation(ctx)
	if err != nil {
		return err
	}
	if config == nil {
		return ErrNotInstalled
	}
	return nil
}

func (m *Manager) start(ctx context.Context) error {
	config, err := m.Inspect(ctx)
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
	if err := m.runner.Run(ctx, "/bin/launchctl", "bootstrap", m.domain(), m.plistPath()); err != nil {
		return fmt.Errorf("register service: %w", err)
	}
	return nil
}

func (m *Manager) stop(ctx context.Context) error {
	if err := m.requireInstallation(ctx); err != nil {
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

func (m *Manager) Start(ctx context.Context) error { return m.mutate(ctx, m.start) }
func (m *Manager) Stop(ctx context.Context) error  { return m.mutate(ctx, m.stop) }
func (m *Manager) Restart(ctx context.Context) error {
	return m.mutate(ctx, func(ctx context.Context) error {
		if err := m.stop(ctx); err != nil {
			return err
		}
		return m.start(ctx)
	})
}

func (m *Manager) Uninstall(ctx context.Context) error {
	return m.mutate(ctx, func(ctx context.Context) error {
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
	})
}
