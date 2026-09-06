//go:build linux || windows

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

type testExit int

func (e testExit) Error() string { return "synthetic service failure" }

func (e testExit) ExitCode() int { return int(e) }

type syntheticRunner struct {
	calls              [][]string
	active, registered bool
	fail               bool
}

func (r *syntheticRunner) Run(ctx context.Context, program string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.calls = append(r.calls, append([]string{program}, args...))
	if r.fail {
		return testExit(5)
	}
	joined := strings.Join(args, " ")
	if filepath.Base(program) == "powershell.exe" {
		if strings.Contains(joined, "$folder.RegisterTask(") {
			r.registered = true
			return nil
		}
		if strings.Contains(joined, ".Run($null)") {
			r.active = true
			return nil
		}
		if strings.Contains(joined, ".Stop(0)") {
			r.active = false
			return nil
		}
		if strings.Contains(joined, "$folder.DeleteTask(") {
			r.registered = false
			return nil
		}
		if !r.registered {
			return testExit(4)
		}
		if r.active {
			return nil
		}
		return testExit(3)
	}
	switch {
	case strings.Contains(joined, "is-enabled"):
		if r.registered {
			return nil
		}
		return testExit(1)
	case strings.Contains(joined, "is-active"):
		if r.active {
			return nil
		}
		return testExit(3)
	case strings.Contains(joined, " enable "):
		r.registered = true
	case strings.Contains(joined, " start "):
		r.active = true
	case strings.Contains(joined, " stop "):
		r.active = false
	case strings.Contains(joined, " disable "):
		r.registered = false
	}
	return nil
}

func portableFixture(t *testing.T) (*Manager, *syntheticRunner, string) {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("user service excludes root")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := privatefs.EnsureDirectory(home); err != nil {
		t.Fatal(err)
	}
	runner := &syntheticRunner{}
	m, err := New(home, os.Geteuid(), runner)
	if err != nil {
		t.Fatal(err)
	}
	name := "version % $ & binaries"
	if runtime.GOOS == "windows" {
		name = "version $ & binaries"
	}
	bin := filepath.Join(home, name)
	if err := privatefs.EnsureDirectory(bin); err != nil {
		t.Fatal(err)
	}
	config := binaryConfig(bin)
	for _, name := range []string{config.Daemon, config.Relay} {
		if err := privatefs.WriteFile(name, []byte("synthetic executable"), false); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(name, 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	return m, runner, bin
}

func TestPortableLifecycleAndCanonicalInstallation(t *testing.T) {
	m, r, bin := portableFixture(t)
	ctx := context.Background()
	config, err := m.Install(ctx, bin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, bin); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("overwrite: %v", err)
	}
	got, err := m.Inspect(ctx)
	if err != nil || got == nil || *got != config {
		t.Fatalf("inspect: %v %v", got, err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(ctx); !errors.Is(err, ErrServiceLoaded) {
		t.Fatalf("live removal: %v", err)
	}
	if err := m.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(config.Daemon); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Inspect(ctx); err == nil {
		t.Fatal("missing binary accepted")
	}
	if err := m.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if r.registered || r.active {
		t.Fatal("registration retained")
	}
	if _, err := os.Stat(config.Relay); err != nil {
		t.Fatal("uninstall removed binaries")
	}
}

func TestPortableRejectsModifiedInstallationAndRunnerFailure(t *testing.T) {
	m, r, bin := portableFixture(t)
	ctx := context.Background()
	if _, err := m.Install(ctx, bin); err != nil {
		t.Fatal(err)
	}
	r.fail = true
	if err := m.Start(ctx); err == nil {
		t.Fatal("runner error hidden")
	}
	r.fail = false
	f, err := os.OpenFile(m.recordPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString("\n")
	if err := errors.Join(writeErr, f.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Inspect(ctx); !errors.Is(err, ErrInvalidInstallation) {
		t.Fatalf("modified installation accepted: %v", err)
	}
	if err := m.Uninstall(ctx); !errors.Is(err, ErrInvalidInstallation) {
		t.Fatalf("modified installation removed: %v", err)
	}
}

func TestPortableStopWaitsForAccountLock(t *testing.T) {
	m, _, bin := portableFixture(t)
	ctx := context.Background()
	if _, err := m.Install(ctx, bin); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := privatefs.EnsureDirectory(filepath.Dir(m.accountLock)); err != nil {
		t.Fatal(err)
	}
	lock, err := daemon.AcquireAccountLock(m.accountLock)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	timeout, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if err := m.Stop(timeout); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop bypassed held account lock: %v", err)
	}
}

func TestPortableInstallResumesAnUnregisteredCanonicalRecord(t *testing.T) {
	m, r, bin := portableFixture(t)
	ctx := context.Background()
	if _, err := m.Install(ctx, bin); err != nil {
		t.Fatal(err)
	}
	// A failed external registration can leave the complete local configuration.
	r.registered = false
	if _, err := m.Install(ctx, bin); err != nil {
		t.Fatalf("registration retry: %v", err)
	}
	if !r.registered {
		t.Fatal("registration was not restored")
	}
	if _, err := m.Install(ctx, bin); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("existing registration: %v", err)
	}
}
