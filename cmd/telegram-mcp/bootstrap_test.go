package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/logging"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

type fakeStartup struct {
	calls int
	start func(context.Context) error
}

func (s *fakeStartup) RequestStart(ctx context.Context, _ time.Time) (service.StartDisposition, error) {
	s.calls++
	if s.start != nil {
		return service.StartJoining, s.start(ctx)
	}
	return service.StartLaunched, nil
}

func TestBootstrapReusesExistingConnectionWithoutLifecycle(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	got, err := connectWithStartup(context.Background(), "unused", func(context.Context, string) (net.Conn, error) {
		return a, nil
	}, func(context.Context, func(startupSession) error) error {
		t.Fatal("existing endpoint acquired a lifecycle lease")
		return nil
	})
	if got != a || err != nil {
		t.Fatalf("connection=%v error=%v", got, err)
	}
}

func TestBootstrapStartsOnceAndHandsOffVerifiedConnection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	session := &fakeStartup{}
	dials := 0
	got, err := connectWithStartup(context.Background(), "unused", func(context.Context, string) (net.Conn, error) {
		dials++
		if dials <= 3 {
			return nil, daemon.ErrDaemonUnavailable
		}
		return a, nil
	}, func(ctx context.Context, action func(startupSession) error) error {
		return action(session)
	})
	if err != nil || got != a || session.calls != 1 || dials != 4 {
		t.Fatalf("connection=%v starts=%d dials=%d error=%v", got, session.calls, dials, err)
	}
}

func TestBootstrapRechecksEndpointAfterLifecycleWait(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	session := &fakeStartup{}
	dials := 0
	got, err := connectWithStartup(context.Background(), "unused", func(context.Context, string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return nil, daemon.ErrDaemonUnavailable
		}
		return a, nil
	}, func(ctx context.Context, action func(startupSession) error) error { return action(session) })
	if err != nil || got != a || session.calls != 0 {
		t.Fatalf("starts=%d error=%v", session.calls, err)
	}
}

func TestBootstrapRefusesUnsafeEndpointsAndDisabledServices(t *testing.T) {
	for _, cause := range []error{daemon.ErrUnsafeSocket, service.ErrDisabled} {
		session := &fakeStartup{start: func(context.Context) error { return cause }}
		_, err := connectWithStartup(context.Background(), "unused", func(context.Context, string) (net.Conn, error) {
			if cause == daemon.ErrUnsafeSocket {
				return nil, cause
			}
			return nil, daemon.ErrDaemonUnavailable
		}, func(ctx context.Context, action func(startupSession) error) error { return action(session) })
		if !errors.Is(err, cause) || (cause == daemon.ErrUnsafeSocket && session.calls != 0) {
			t.Fatalf("cause=%v starts=%d error=%v", cause, session.calls, err)
		}
	}
}

func TestBootstrapTimeoutDoesNotSubmitAnotherStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	session := &fakeStartup{}
	_, err := connectWithStartup(ctx, "unused", func(context.Context, string) (net.Conn, error) {
		return nil, daemon.ErrDaemonUnavailable
	}, func(ctx context.Context, action func(startupSession) error) error { return action(session) })
	if !errors.Is(err, context.DeadlineExceeded) || session.calls != 1 {
		t.Fatalf("starts=%d error=%v", session.calls, err)
	}
}

func TestBootstrapMaintenanceDeadlineAndReleaseFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := connectWithStartup(ctx, "unused", func(context.Context, string) (net.Conn, error) {
		return nil, daemon.ErrDaemonUnavailable
	}, func(ctx context.Context, _ func(startupSession) error) error { <-ctx.Done(); return ctx.Err() })
	if startupReason(err) != logging.StartupMaintenanceBusy {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	dials := 0
	failedRelease := errors.New("synthetic release failure")
	_, err = connectWithStartup(context.Background(), "unused", func(context.Context, string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return nil, daemon.ErrDaemonUnavailable
		}
		return a, nil
	}, func(ctx context.Context, action func(startupSession) error) error {
		return errors.Join(action(&fakeStartup{}), failedRelease)
	})
	if !errors.Is(err, failedRelease) {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("closed")); err == nil {
		t.Fatal("connection retained after lease release failed")
	}
}

func TestStartupDeadlineCapsInheritedBudget(t *testing.T) {
	t.Setenv(startupDeadlineEnvironment, "9223372036854775807")
	before := time.Now()
	deadline, err := startupDeadline()
	if err != nil || deadline.Before(before) || deadline.After(time.Now().Add(startupTimeout)) {
		t.Fatalf("deadline=%v error=%v", deadline, err)
	}
	t.Setenv(startupDeadlineEnvironment, "secret\n")
	if _, err := startupDeadline(); startupReason(err) != logging.StartupStateInvalid {
		t.Fatal(err)
	}
}

func TestMissingHumanInstallationDoesNotCreateRuntimeState(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("user services exclude root")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := privatefs.EnsureDirectory(home); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"APPDATA":         filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(home, "AppData", "Local"),
	} {
		t.Setenv(name, value)
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	_, err = connectDaemon(context.Background(), paths)
	if !errors.Is(err, service.ErrNotInstalled) {
		t.Fatal(err)
	}
	for _, path := range []string{paths.StateDir, paths.RuntimeDir} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("startup created state without an installation: %v", err)
		}
	}
}
