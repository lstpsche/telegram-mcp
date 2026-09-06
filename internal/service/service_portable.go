//go:build linux || windows

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
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

func (m *Manager) mutate(ctx context.Context, action func(context.Context) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := privatefs.EnsureDirectory(m.stateDir()); err != nil {
		return err
	}
	lock, err := daemon.AcquireAccountLock(filepath.Join(m.stateDir(), "service.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	return action(ctx)
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

func (m *Manager) Install(ctx context.Context, dir string) (config Config, err error) {
	err = m.mutate(ctx, func(ctx context.Context) error {
		if _, err := os.Lstat(m.recordPath()); err == nil {
			existing, err := m.Inspect(ctx)
			if err != nil {
				return err
			}
			if existing == nil || existing.BinDir != dir {
				return ErrAlreadyInstalled
			}
			registered, err := m.registrationExists(ctx)
			if err != nil {
				return err
			}
			if registered {
				return ErrAlreadyInstalled
			}
			config = *existing
			return m.activateRegistration(ctx)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var err error
		config, err = m.inspectBinaries(ctx, dir)
		if err != nil {
			return err
		}
		return m.installRegistration(ctx, config)
	})
	return config, err
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

func (m *Manager) start(ctx context.Context) error {
	config, err := m.Inspect(ctx)
	if err != nil {
		return err
	}
	if config == nil {
		return ErrNotInstalled
	}
	running, err := m.running(ctx)
	if err != nil {
		return err
	}
	if running {
		return ErrServiceLoaded
	}
	return m.startRegistration(ctx)
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
			return m.waitAccountRelease(ctx)
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

func (m *Manager) Stop(ctx context.Context) error { return m.mutate(ctx, m.stop) }

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
	})
}

func exited(err error, code int) bool {
	var status interface{ ExitCode() int }
	return errors.As(err, &status) && status.ExitCode() == code
}

// Task Scheduler can return before the daemon process has actually exited.
func (m *Manager) waitAccountRelease(ctx context.Context) error {
	if _, err := os.Lstat(m.accountLock); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for {
		lock, err := daemon.AcquireAccountLock(m.accountLock)
		if err == nil {
			return lock.Release()
		}
		if !errors.Is(err, daemon.ErrAccountLocked) {
			return err
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
