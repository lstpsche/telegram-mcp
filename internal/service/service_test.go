//go:build darwin

package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
)

type exitStatus int

func (e exitStatus) Error() string { return "synthetic process status" }

func (e exitStatus) ExitCode() int { return int(e) }

type fakeRunner struct {
	mu     sync.Mutex
	loaded bool
	calls  [][]string
	fail   func(string, []string) error
}

func (r *fakeRunner) Run(ctx context.Context, program string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.calls = append(r.calls, append([]string{program}, args...))
	if r.fail != nil {
		if err := r.fail(program, args); err != nil {
			return err
		}
	}
	switch args[0] {
	case "print":
		if strings.Count(args[1], "/") == 2 && !r.loaded {
			return exitStatus(113)
		}
	case "bootstrap":
		r.loaded = true
	case "bootout":
		r.loaded = false
	default:
		return errors.New("unexpected command")
	}
	return nil
}

func fixture(t *testing.T) (*Manager, *fakeRunner, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "binaries & ü <stable>")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"telegram-mcp", "telegram-mcpd", "telegram-mcpctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("synthetic executable"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeRunner{}
	manager, err := New(home, os.Geteuid(), runner)
	if err != nil {
		t.Fatal(err)
	}
	return manager, runner, bin
}

func install(t *testing.T, m *Manager, bin string) Config {
	t.Helper()
	config, err := m.Install(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestLifecyclePreservesStateAndUsesExactLaunchdTargets(t *testing.T) {
	m, r, bin := fixture(t)
	config := install(t, m, bin)
	got, err := m.Inspect(context.Background())
	if err != nil || got == nil || *got != config {
		t.Fatalf("inspect: %#v %v", got, err)
	}
	data, err := os.ReadFile(m.plistPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("&amp;")) || !bytes.Contains(data, []byte("&lt;stable&gt;")) {
		t.Fatal("paths not escaped")
	}
	if err := exec.Command("/usr/bin/plutil", "-lint", m.plistPath()).Run(); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(m.home, "Library", "Application Support", "Telegram MCP", "metadata.db")
	if err := os.WriteFile(state, []byte("preserved metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(context.Background()); !errors.Is(err, ErrServiceLoaded) {
		t.Fatalf("uninstall live: %v", err)
	}
	if err := m.Start(context.Background()); !errors.Is(err, ErrServiceLoaded) {
		t.Fatalf("duplicate start: %v", err)
	}
	if err := m.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background()); !errors.Is(err, ErrServiceAbsent) {
		t.Fatalf("duplicate stop: %v", err)
	}
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("duplicate uninstall: %v", err)
	}
	if got, err := os.ReadFile(state); err != nil || string(got) != "preserved metadata" {
		t.Fatalf("metadata changed: %q %v", got, err)
	}
	if _, err := os.Stat(config.Daemon); err != nil {
		t.Fatal(err)
	}
	var mutations [][]string
	for _, call := range r.calls {
		if len(call) > 1 && (call[1] == "bootstrap" || call[1] == "bootout") {
			mutations = append(mutations, call)
		}
	}
	want := [][]string{{"/bin/launchctl", "bootstrap", m.domain(), m.plistPath()}, {"/bin/launchctl", "bootout", m.target()}, {"/bin/launchctl", "bootstrap", m.domain(), m.plistPath()}, {"/bin/launchctl", "bootout", m.target()}}
	if !reflect.DeepEqual(mutations, want) {
		t.Fatalf("commands: %#v", mutations)
	}
}

func TestInspectAbsentDoesNotCreateState(t *testing.T) {
	m, _, _ := fixture(t)
	got, err := m.Inspect(context.Background())
	if err != nil || got != nil {
		t.Fatalf("inspect: %v %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(m.home, "Library")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created state: %v", err)
	}
}

func TestInstallRefusesExistingAndUntrustedFiles(t *testing.T) {
	tests := map[string]func(*testing.T, *Manager, string){
		"binary missing": func(t *testing.T, m *Manager, bin string) { must(t, os.Remove(filepath.Join(bin, "telegram-mcp"))) },
		"binary symlink": func(t *testing.T, m *Manager, bin string) {
			path := filepath.Join(bin, "telegram-mcp")
			must(t, os.Remove(path))
			must(t, os.Symlink(filepath.Join(bin, "telegram-mcpd"), path))
		},
		"binary writable": func(t *testing.T, m *Manager, bin string) {
			must(t, os.Chmod(filepath.Join(bin, "telegram-mcp"), 0777))
		},
		"directory public": func(t *testing.T, m *Manager, bin string) { must(t, os.Chmod(bin, 0755)) },
		"launch agents symlink": func(t *testing.T, m *Manager, bin string) {
			must(t, os.Mkdir(filepath.Join(m.home, "Library"), 0700))
			must(t, os.Symlink(bin, filepath.Dir(m.plistPath())))
		},
		"existing plist": func(t *testing.T, m *Manager, bin string) {
			must(t, os.MkdirAll(filepath.Dir(m.plistPath()), 0700))
			must(t, os.WriteFile(m.plistPath(), []byte("foreign"), 0600))
		},
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			m, r, bin := fixture(t)
			alter(t, m, bin)
			if _, err := m.Install(context.Background(), bin); err == nil {
				t.Fatal("unsafe install accepted")
			}
			if r.loaded {
				t.Fatal("loaded service")
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestInspectRejectsAlteredPlistAndBinaries(t *testing.T) {
	for _, alter := range []string{"foreign", "mode", "oversized", "symlink", "binary removed"} {
		t.Run(alter, func(t *testing.T) {
			m, _, bin := fixture(t)
			install(t, m, bin)
			switch alter {
			case "foreign":
				must(t, os.WriteFile(m.plistPath(), bytes.ReplaceAll(m.plist(Config{Daemon: filepath.Join(bin, "telegram-mcpd")}), []byte("<false/>"), []byte("<true/>")), 0600))
			case "mode":
				must(t, os.Chmod(m.plistPath(), 0644))
			case "oversized":
				must(t, os.WriteFile(m.plistPath(), bytes.Repeat([]byte("x"), 16385), 0600))
			case "symlink":
				must(t, os.Remove(m.plistPath()))
				must(t, os.Symlink(filepath.Join(bin, "telegram-mcpd"), m.plistPath()))
			case "binary removed":
				must(t, os.Remove(filepath.Join(bin, "telegram-mcpd")))
			}
			if _, err := m.Inspect(context.Background()); err == nil {
				t.Fatal("invalid installation accepted")
			}
		})
	}
}

func TestLaunchctlAbsenceIsSpecificAndDomainMustExist(t *testing.T) {
	for _, tc := range []struct {
		name   string
		domain bool
		code   int
	}{{"missing domain", true, 113}, {"other exit", false, 1}, {"unknown exit", false, 114}} {
		t.Run(tc.name, func(t *testing.T) {
			m, r, bin := fixture(t)
			r.fail = func(program string, args []string) error {
				if program == "/bin/launchctl" && args[0] == "print" && (strings.Count(args[1], "/") == 1) == tc.domain {
					return exitStatus(tc.code)
				}
				return nil
			}
			if _, err := m.Install(context.Background(), bin); err == nil {
				t.Fatal("process failure accepted as absence")
			}
			if _, err := os.Lstat(m.plistPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("plist created: %v", err)
			}
		})
	}
}

func TestCommandFailuresPreserveCauseAndDoNotContinue(t *testing.T) {
	for _, command := range []string{"bootstrap", "bootout"} {
		t.Run(command, func(t *testing.T) {
			m, r, bin := fixture(t)
			cause := errors.New("synthetic failure")
			install(t, m, bin)
			if command == "bootout" {
				must(t, m.Start(context.Background()))
			}
			r.fail = func(program string, args []string) error {
				if args[0] == command {
					return cause
				}
				return nil
			}
			var err error
			switch command {
			case "bootstrap":
				err = m.Start(context.Background())
			case "bootout":
				err = m.Restart(context.Background())
			}
			if !errors.Is(err, cause) {
				t.Fatalf("cause lost: %v", err)
			}
			if command == "bootout" && r.calls[len(r.calls)-1][1] != "bootout" {
				t.Fatal("continued after stop failed")
			}
		})
	}
}

func TestCanceledOperationsAndConcurrentLock(t *testing.T) {
	m, _, bin := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Install(ctx, bin); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(m.home, "Library")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancellation created state: %v", err)
	}
	install(t, m, bin)
	lock, err := m.lock()
	must(t, err)
	if err := m.Start(context.Background()); !errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatalf("parallel mutation: %v", err)
	}
	must(t, lock.Release())
}

func TestStopWaitsForConfirmedRemovalAndHonorsDeadline(t *testing.T) {
	m, r, bin := fixture(t)
	install(t, m, bin)
	must(t, m.Start(context.Background()))
	r.fail = func(program string, args []string) error {
		if program == "/bin/launchctl" && args[0] == "print" && strings.Count(args[1], "/") == 2 {
			r.loaded = true
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := m.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop without confirmation: %v", err)
	}
}

func TestCleanupDoesNotRequireOldBinariesToRemainUsable(t *testing.T) {
	for _, damage := range []string{"missing binary", "unsafe binary permissions"} {
		t.Run(damage, func(t *testing.T) {
			m, r, bin := fixture(t)
			install(t, m, bin)
			must(t, m.Start(context.Background()))
			if damage == "missing binary" {
				must(t, os.Remove(filepath.Join(bin, "telegram-mcpd")))
			}
			if damage == "unsafe binary permissions" {
				must(t, os.Chmod(filepath.Join(bin, "telegram-mcpd"), 0777))
			}
			if _, err := m.Inspect(context.Background()); err == nil {
				t.Fatal("damaged installation reported usable")
			}
			callsBefore := len(r.calls)
			must(t, m.Stop(context.Background()))
			must(t, m.Uninstall(context.Background()))
			for _, call := range r.calls[callsBefore:] {
				if call[0] == "/usr/bin/codesign" {
					t.Fatal("cleanup verified unavailable binary")
				}
			}
		})
	}
}

func TestRestartStopsBeforeReportingDamagedBinary(t *testing.T) {
	m, r, bin := fixture(t)
	install(t, m, bin)
	must(t, m.Start(context.Background()))
	must(t, os.Remove(filepath.Join(bin, "telegram-mcpd")))
	if err := m.Restart(context.Background()); err == nil {
		t.Fatal("restart accepted missing binary")
	}
	if r.loaded {
		t.Fatal("damaged service remained registered")
	}
}
