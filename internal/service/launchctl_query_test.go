//go:build darwin

package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
)

func TestDisabledServiceQueryRequiresKnownGrammar(t *testing.T) {
	for _, tc := range []struct {
		data     string
		disabled bool
	}{
		{"disabled services = (no disabled services)", false},
		{"\n\tdisabled services = (no disabled services)\n", false},
		{"disabled services = {\n}", false},
		{"disabled services = {\n \"other.service\" => disabled\n}", false},
		{"disabled services = {\n \"" + Label + "\" => disabled\n}", true},
		{"disabled services = {\n \"" + Label + "\" => enabled\n}", false},
	} {
		got, err := parseDisabledServices([]byte(tc.data))
		if err != nil || got != tc.disabled {
			t.Fatal(got, err)
		}
	}
	for _, data := range []string{
		"",
		"unknown = {\n}",
		"disabled services = (unknown)",
		"disabled services = (no disabled services) trailing",
		"disabled services = (no disabled services)\ntrailing",
		"disabled services = {\n \"service\" => unknown\n}",
		"disabled services = {\n \"service\" => disabled\n \"service\" => enabled\n}",
		"disabled services = {\n}\ntrailing",
		"disabled services = {\n}\nlogin item associations = {\n}",
		strings.Repeat("x", maxQueryBytes+1),
	} {
		if _, err := parseDisabledServices([]byte(data)); !errors.Is(err, ErrServiceUnavailable) {
			t.Fatal("unsupported state accepted", err)
		}
	}
}

func TestAutomaticStartWakesLoadedJobAndPreservesPause(t *testing.T) {
	m, r, bin := fixture(t)
	install(t, m, bin)
	r.loaded = true // The job remains loaded after a process has exited.
	before := len(r.calls)
	now := time.Now()
	if err := m.WithSession(context.Background(), func(s *Session) error {
		disposition, err := s.RequestStart(context.Background(), now)
		if err != nil || disposition != StartLaunched {
			t.Fatal(disposition, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := r.calls[before:]
	if calls[len(calls)-1][1] != "kickstart" || len(calls[len(calls)-1]) != 3 {
		t.Fatal(calls)
	}
	for _, call := range calls {
		if call[1] == "enable" || call[1] == "disable" || call[1] == "bootout" {
			t.Fatal("automatic lifecycle mutation", call)
		}
	}
	before = len(r.calls)
	if err := m.WithSession(context.Background(), func(s *Session) error {
		disposition, err := s.RequestStart(context.Background(), now.Add(time.Second))
		if err != nil || disposition != StartJoining {
			t.Fatal(disposition, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls[before:] {
		if call[1] == "kickstart" || call[1] == "bootstrap" {
			t.Fatal("recent start repeated", call)
		}
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	before = len(r.calls)
	if err := m.WithSession(context.Background(), func(s *Session) error {
		_, err := s.RequestStart(context.Background(), now.Add(time.Minute))
		return err
	}); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	for _, call := range r.calls[before:] {
		if call[1] == "enable" || call[1] == "kickstart" || call[1] == "bootstrap" {
			t.Fatal("pause bypassed", call)
		}
	}
}

func TestDarwinStopWaitsForAccountAfterJobRemoval(t *testing.T) {
	m, r, bin := fixture(t)
	install(t, m, bin)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	lock, err := daemon.AcquireAccountLock(m.accountPath())
	must(t, err)
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := m.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !r.disabled || r.loaded {
		t.Fatal("OS stop intent was not retained")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := m.Uninstall(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("removed registration with live account", err)
	}
	if _, err := os.Stat(m.plistPath()); err != nil {
		t.Fatal("registration removed", err)
	}
	must(t, lock.Release())
	must(t, m.Stop(context.Background()))
	must(t, m.Uninstall(context.Background()))
}

func TestDisabledDarwinInstallPublishesAfterDisable(t *testing.T) {
	m, r, bin := fixture(t)
	r.fail = func(_ string, args []string) error {
		if args[0] == "disable" {
			if _, err := os.Stat(m.plistPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("disabled registration exposed before pause", err)
			}
		}
		return nil
	}
	if err := m.WithSession(context.Background(), func(s *Session) error { _, err := s.InstallWithState(context.Background(), bin, false); return err }); err != nil {
		t.Fatal(err)
	}
	if !r.disabled || r.loaded {
		t.Fatal("disabled install became runnable")
	}
}

// This opt-in probe inspects only current-user OS enablement. It neither loads
// account state nor installs, enables, starts or stops a service.
func TestNativeLaunchctlDisabledQuery(t *testing.T) {
	if os.Getenv("TELEGRAM_MCP_NATIVE_SERVICE_QUERY_TEST") != "1" {
		t.Skip("requires a logged-in macOS GUI session")
	}
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := m.enabled(ctx); err != nil {
		t.Fatal("current launchctl query grammar is unsupported", err)
	}
}
