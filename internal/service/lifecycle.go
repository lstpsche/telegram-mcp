package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

const operationTimeout = 30 * time.Second
const startCoalescingWindow = 30 * time.Second

// Session serializes one human operation or one bounded automatic startup.
// It is valid only inside its WithSession callback.
type Session struct {
	manager *Manager
	active  atomic.Bool
}

// WithSession waits for the existing lifecycle lock. Its acquisition deadline
// does not limit the callback's interactive work; callers bound each operation.
func (m *Manager) WithSession(ctx context.Context, action func(*Session) error) (result error) {
	acquire, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	for {
		if err := acquire.Err(); err != nil {
			return errors.Join(daemon.ErrAccountLocked, err)
		}
		lock, err := m.lock()
		if err == nil {
			defer func() { result = errors.Join(result, lock.Release()) }()
			if err := acquire.Err(); err != nil {
				return err
			}
			session := &Session{manager: m}
			session.active.Store(true)
			defer session.active.Store(false)
			return action(session)
		}
		if !errors.Is(err, daemon.ErrAccountLocked) {
			return err
		}
		if err := waitLifecycle(acquire); err != nil {
			return errors.Join(daemon.ErrAccountLocked, err)
		}
	}
}

func waitLifecycle(ctx context.Context) error {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Session) check(ctx context.Context) error {
	if s == nil || !s.active.Load() {
		return ErrSessionClosed
	}
	return ctx.Err()
}

func (s *Session) Inspect(ctx context.Context) (*Config, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	return s.manager.Inspect(ctx)
}

func (s *Session) Install(ctx context.Context, directory string) (Config, error) {
	return s.InstallWithState(ctx, directory, true)
}

func (s *Session) InstallWithState(ctx context.Context, directory string, enabled bool) (Config, error) {
	if err := s.check(ctx); err != nil {
		return Config{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return s.manager.install(ctx, directory, enabled)
}

func (s *Session) Enabled(ctx context.Context) (bool, error) {
	if err := s.check(ctx); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.manager.requireRecord(ctx); err != nil {
		return false, err
	}
	return s.manager.enabled(ctx)
}

func (s *Session) SetEnabled(ctx context.Context, enabled bool) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.manager.requireRecord(ctx); err != nil {
		return err
	}
	return s.manager.setEnabled(ctx, enabled)
}

func (s *Session) Start(ctx context.Context) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	config, err := s.manager.Inspect(ctx)
	if err != nil {
		return err
	}
	if config == nil {
		return ErrNotInstalled
	}
	if err := s.manager.setEnabled(ctx, true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.manager.reserveStart(time.Now(), true); err != nil {
		return err
	}
	return s.manager.wake(ctx)
}

// Quiesce disables future activation before stopping the current runtime.
// Callers retain any prior enablement they intend to restore on success.
func (s *Session) Quiesce(ctx context.Context) error { return s.Stop(ctx) }

func (s *Session) Stop(ctx context.Context) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.manager.requireRecord(ctx); err != nil {
		return err
	}
	if err := s.manager.setEnabled(ctx, false); err != nil && !errors.Is(err, ErrServiceAbsent) {
		return err
	}
	if err := s.manager.stop(ctx); err != nil && !errors.Is(err, ErrServiceAbsent) {
		return err
	}
	return s.manager.waitAccountRelease(ctx)
}

func (s *Session) Restart(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start(ctx)
}

func (s *Session) Uninstall(ctx context.Context) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return s.manager.uninstall(ctx)
}

// RequestStart never changes enablement or retries a launch. The caller holds
// this session while waiting for the resulting local transport connection.
func (s *Session) RequestStart(ctx context.Context, now time.Time) (StartDisposition, error) {
	if err := s.check(ctx); err != nil {
		return "", err
	}
	config, err := s.manager.Inspect(ctx)
	if err != nil {
		return "", err
	}
	if config == nil {
		return "", ErrNotInstalled
	}
	enabled, err := s.manager.enabled(ctx)
	if err != nil {
		return "", err
	}
	if !enabled {
		return "", ErrDisabled
	}
	busy, err := s.manager.accountBusy()
	if err != nil {
		return "", err
	}
	if busy {
		return StartJoining, nil
	}
	active, err := s.manager.startActive(ctx)
	if err != nil {
		return "", err
	}
	if active {
		return StartJoining, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	reserved, err := s.manager.reserveStart(now, false)
	if err != nil {
		return "", err
	}
	if !reserved {
		return StartJoining, nil
	}
	if err := s.manager.wake(ctx); err != nil {
		return "", err
	}
	return StartLaunched, nil
}

func (m *Manager) startRecordPath() string { return filepath.Join(m.stateDir(), "service-start.json") }

type startRecord struct {
	Schema      int   `json:"schema"`
	LastAttempt int64 `json:"last_attempt_unix_ms"`
}

func (m *Manager) reserveStart(now time.Time, override bool) (bool, error) {
	millis := now.UnixMilli()
	if millis < 0 {
		return false, ErrStartupState
	}
	data, err := privatefs.ReadFile(m.startRecordPath(), 1024)
	if err == nil {
		var record startRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return false, errors.Join(ErrStartupState, err)
		}
		expected, _ := json.Marshal(record)
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil || !bytes.Equal(expected, compact.Bytes()) || record.Schema != 1 || record.LastAttempt < 0 {
			return false, ErrStartupState
		}
		if !override {
			if record.LastAttempt > millis {
				return false, ErrStartupState
			}
			if millis-record.LastAttempt < startCoalescingWindow.Milliseconds() {
				return false, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, errors.Join(ErrStartupState, err)
	}
	data, _ = json.Marshal(startRecord{Schema: 1, LastAttempt: millis})
	if err := privatefs.WriteFile(m.startRecordPath(), data, true); err != nil {
		return false, errors.Join(ErrStartupState, err)
	}
	return true, nil
}

func (m *Manager) accountBusy() (bool, error) {
	if _, err := os.Lstat(m.accountPath()); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	lock, err := daemon.AcquireAccountLock(m.accountPath())
	if errors.Is(err, daemon.ErrAccountLocked) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, lock.Release()
}

func (m *Manager) operation(ctx context.Context, action func(context.Context, *Session) error) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return m.WithSession(ctx, func(s *Session) error { return action(ctx, s) })
}

func (m *Manager) Install(ctx context.Context, directory string) (config Config, result error) {
	result = m.operation(ctx, func(ctx context.Context, s *Session) error {
		var err error
		config, err = s.Install(ctx, directory)
		return err
	})
	return config, result
}
func (m *Manager) Start(ctx context.Context) error {
	return m.operation(ctx, func(ctx context.Context, s *Session) error { return s.Start(ctx) })
}
func (m *Manager) Stop(ctx context.Context) error {
	return m.operation(ctx, func(ctx context.Context, s *Session) error { return s.Stop(ctx) })
}
func (m *Manager) Restart(ctx context.Context) error {
	return m.operation(ctx, func(ctx context.Context, s *Session) error { return s.Restart(ctx) })
}
func (m *Manager) Uninstall(ctx context.Context) error {
	return m.operation(ctx, func(ctx context.Context, s *Session) error { return s.Uninstall(ctx) })
}

// Task Scheduler and launchd can report removal before the process releases its
// account lock. Human maintenance cannot begin until ownership is available.
func (m *Manager) waitAccountRelease(ctx context.Context) error {
	if _, err := os.Lstat(m.accountPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for {
		busy, err := m.accountBusy()
		if err != nil {
			return err
		}
		if !busy {
			return nil
		}
		if err := waitLifecycle(ctx); err != nil {
			return err
		}
	}
}
