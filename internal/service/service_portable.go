//go:build linux || windows

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
)

// Manager controls a per-user service without administrative privileges.
type Manager struct {
	home        string
	uid         int
	runner      Runner
	accountLock string
	storageDir  string
	cacheRoot   string
}

func New(home string, uid int, runner Runner) (*Manager, error) {
	if runner == nil {
		return nil, errors.New("service requires a process runner")
	}
	if err := validatePath(home); err != nil {
		return nil, err
	}
	if err := validateUser(home, uid); err != nil {
		return nil, err
	}
	return &Manager{home: home, uid: uid, runner: runner, accountLock: defaultAccountLock(home), cacheRoot: filepath.Join(home, ".cache")}, nil
}

func Default() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	manager, err := New(home, os.Geteuid(), processRunner{})
	if err != nil {
		return nil, err
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return nil, err
	}
	manager.accountLock = paths.Lock
	manager.storageDir = paths.StateDir
	manager.cacheRoot = filepath.Dir(paths.RuntimeDir)
	return manager, nil
}

func (m *Manager) read(ctx context.Context) (*Config, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := m.readRegistration(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := validatePath(config.BinDir); err != nil {
		return nil, err
	}

	return &config, nil
}

func (m *Manager) Inspect(ctx context.Context) (*Config, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	config, err := m.read(ctx)
	if err != nil || config == nil {
		return nil, err
	}
	verified, err := m.inspectBinaries(ctx, config.BinDir)
	if err != nil {
		return nil, err
	}
	return &verified, nil
}

func (m *Manager) install(ctx context.Context, dir string, enabled bool) (Config, error) {
	if _, err := os.Lstat(m.recordPath()); err == nil {
		existing, err := m.Inspect(ctx)
		if err != nil {
			return Config{}, err
		}
		if existing == nil || existing.BinDir != dir {
			return Config{}, ErrAlreadyInstalled
		}
		registered, err := m.registrationExists(ctx)
		if err != nil {
			return Config{}, err
		}
		if registered {
			return Config{}, ErrAlreadyInstalled
		}
		return *existing, m.activateRegistrationState(ctx, enabled)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	config, err := m.inspectBinaries(ctx, dir)
	if err != nil {
		return Config{}, err
	}
	return config, m.installRegistrationState(ctx, config, enabled)
}

func (m *Manager) require(ctx context.Context) (*Config, error) {
	config, err := m.read(ctx)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, ErrNotInstalled
	}
	return config, nil
}

func (m *Manager) stop(ctx context.Context) error {
	if _, err := m.require(ctx); err != nil {
		return err
	}
	running, err := m.running(ctx)
	if err != nil {
		return err
	}
	if !running {
		return ErrServiceAbsent
	}
	if err := m.stopRegistration(ctx); err != nil {
		return err
	}
	for {
		running, err = m.running(ctx)
		if err != nil {
			return err
		}
		if !running {
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
	if _, err := m.require(ctx); err != nil {
		return err
	}
	running, err := m.running(ctx)
	if err != nil {
		return err
	}
	if running {
		return ErrServiceLoaded
	}
	if err := m.waitAccountRelease(ctx); err != nil {
		return err
	}
	return m.removeRegistration(ctx)
}

func exited(err error, code int) bool {
	var status interface{ ExitCode() int }
	return errors.As(err, &status) && status.ExitCode() == code
}

func (m *Manager) requireRecord(ctx context.Context) error { _, err := m.require(ctx); return err }
func (m *Manager) accountPath() string                     { return m.accountLock }
