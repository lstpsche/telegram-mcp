package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func TestLifecycleSessionWaitsAndReleases(t *testing.T) {
	m := lifecycleFixture(t)
	var retained *Session
	cause := errors.New("synthetic callback failure")
	if err := m.WithSession(context.Background(), func(s *Session) error { retained = s; return cause }); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := retained.Inspect(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatal("escaped session accepted", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.WithSession(context.Background(), func(*Session) error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	called := false
	if err := m.WithSession(ctx, func(*Session) error { called = true; return nil }); !errors.Is(err, daemon.ErrAccountLocked) || !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("concurrent session: %v called=%t", err, called)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := m.WithSession(context.Background(), func(*Session) error { return nil }); err != nil {
		t.Fatal("lease retained", err)
	}
}

func TestLifecycleSessionDoesNotLimitInteractiveWork(t *testing.T) {
	m := lifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := m.WithSession(ctx, func(s *Session) error {
		cancel()
		if _, err := s.Inspect(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStartReservationsAreStrictAndShared(t *testing.T) {
	m := lifecycleFixture(t)
	now := time.UnixMilli(100000)
	reserve := func(at time.Time, override bool) (reserved bool, result error) {
		result = m.WithSession(context.Background(), func(*Session) error { var err error; reserved, err = m.reserveStart(at, override); return err })
		return
	}
	if reserved, err := reserve(now, false); err != nil || !reserved {
		t.Fatal(reserved, err)
	}
	if reserved, err := reserve(now.Add(time.Second), false); err != nil || reserved {
		t.Fatal(reserved, err)
	}
	if _, err := reserve(now.Add(-time.Second), false); !errors.Is(err, ErrStartupState) {
		t.Fatal("clock rollback accepted", err)
	}
	if reserved, err := reserve(now.Add(-time.Second), true); err != nil || !reserved {
		t.Fatal("explicit start blocked", reserved, err)
	}
	if reserved, err := reserve(now.Add(startCoalescingWindow), false); err != nil || !reserved {
		t.Fatal(reserved, err)
	}
	data, err := privatefs.ReadFile(m.startRecordPath(), 1024)
	if err != nil || string(data) != "{\"schema\":1,\"last_attempt_unix_ms\":130000}" {
		t.Fatal(string(data), err)
	}
	for _, bad := range []string{"", `{"schema":2,"last_attempt_unix_ms":0}`, `{"schema":1}`, `{"schema":1,"last_attempt_unix_ms":-1}`, `{"schema":1,"schema":1,"last_attempt_unix_ms":0}`, `{"schema":1,"last_attempt_unix_ms":0,"body":"hostile"}`, `{"schema":1,"last_attempt_unix_ms":0} {}`} {
		if err := privatefs.WriteFile(m.startRecordPath(), []byte(bad), true); err != nil {
			t.Fatal(err)
		}
		for _, override := range []bool{false, true} {
			if _, err := reserve(now, override); !errors.Is(err, ErrStartupState) {
				t.Fatalf("invalid record accepted: %q: %v", bad, err)
			}
		}
	}
}

func TestStartReservationRejectsOversizedPrivateRecord(t *testing.T) {
	m := lifecycleFixture(t)
	if err := m.WithSession(context.Background(), func(*Session) error {
		if err := privatefs.WriteFile(m.startRecordPath(), make([]byte, 1025), false); err != nil {
			return err
		}
		_, err := m.reserveStart(time.Now(), false)
		if !errors.Is(err, ErrStartupState) {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledSessionDoesNotCreateState(t *testing.T) {
	m := lifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.WithSession(ctx, func(*Session) error { t.Fatal("called"); return nil }); !errors.Is(err, context.Canceled) || errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(m.stateDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled acquisition created state", err)
	}
}

func TestLifecycleLockNeedsNoServiceManagerAndReleasesAfterFailure(t *testing.T) {
	state := filepath.Join(t.TempDir(), "account state")
	cause := errors.New("synthetic offline account failure")
	err := WithLifecycleLock(context.Background(), state, func() error {
		lock, err := daemon.AcquireAccountLock(filepath.Join(state, "service.lock"))
		if lock != nil || !errors.Is(err, daemon.ErrAccountLocked) {
			t.Fatal("offline operation did not own the shared lifecycle lock", err)
		}
		return cause
	})
	if !errors.Is(err, cause) {
		t.Fatal("callback failure was lost", err)
	}
	if err := WithLifecycleLock(context.Background(), state, func() error { return nil }); err != nil {
		t.Fatal("offline callback retained lifecycle ownership", err)
	}
}

func TestLifecycleSessionReleaseAfterPanic(t *testing.T) {
	m := lifecycleFixture(t)
	var retained *Session
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic was suppressed")
			}
		}()
		_ = m.WithSession(context.Background(), func(s *Session) error { retained = s; panic("synthetic panic") })
	}()
	if _, err := retained.Inspect(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatal("session survived panic", err)
	}
	if err := m.WithSession(context.Background(), func(*Session) error { return nil }); err != nil {
		t.Fatal("panic retained lock", err)
	}
}
