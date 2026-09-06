package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

// This opt-in test registers a real per-user task. Run only in a disposable
// Windows login session with no existing Telegram MCP task.
func TestNativeScheduledTask(t *testing.T) {
	mode := os.Getenv("TELEGRAM_MCP_NATIVE_SERVICE_TEST")
	if mode != "1" && mode != "registration" {
		t.Skip("requires a disposable Windows login session")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := privatefs.EnsureDirectory(home); err != nil {
		t.Fatal(err)
	}
	m, err := New(home, os.Geteuid(), nativeTaskRunner{t})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	state, err := m.taskState(ctx)
	if err != nil || state != 4 {
		t.Fatalf("requires no existing task: state=%d error=%v", state, err)
	}
	bin := filepath.Join(home, "synthetic café 测试 binaries")
	if err := privatefs.EnsureDirectory(bin); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "started")
	source := filepath.Join(base, "main.go")
	program := fmt.Sprintf("package main\nimport (\"os\"; \"time\")\nfunc main() { if err := os.WriteFile(%q, []byte(\"started\"), 0600); err != nil { os.Exit(1) }; for { time.Sleep(time.Second) } }\n", marker)
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(base, "helper.exe")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", built, source).CombinedOutput(); err != nil {
		t.Fatalf("build synthetic process: %v: %s", err, output)
	}
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	config := binaryConfig(bin)
	for _, path := range []string{config.Relay, config.Control, config.Daemon} {
		if err := privatefs.WriteFile(path, data, false); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		running, err := m.running(cleanup)
		if err != nil {
			t.Error(err)
			return
		}
		if running {
			if err := m.Stop(cleanup); err != nil {
				t.Error(err)
				return
			}
		}
		if _, err := os.Stat(m.recordPath()); err == nil {
			if err := m.Uninstall(cleanup); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := m.Install(ctx, bin); err != nil {
		t.Fatal(err)
	}
	// Read back the scheduler's action, including Unicode, rather than only the local record.
	if err := m.runTaskScript(ctx, `$path=$folder.GetTask($name).Definition.Actions.Item(1).Path
    $encoded=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($path))
    if ($encoded -cne '`+base64.StdEncoding.EncodeToString([]byte(config.Daemon))+`') { exit 6 }`); err != nil {
		t.Fatal(err)
	}
	t.Run("interactive_lifecycle", func(t *testing.T) {
		if mode != "1" {
			t.Skip("requires an interactive desktop login; CI verifies registration and removal")
		}
		waitStarted := func() {
			t.Helper()
			for {
				if _, err := os.Stat(marker); err == nil {
					return
				} else if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				select {
				case <-ctx.Done():
					diagnostic, stop := context.WithTimeout(context.Background(), 10*time.Second)
					defer stop()
					name, nameErr := m.taskName()
					if nameErr != nil {
						t.Fatal(nameErr)
					}
					powershell, programErr := systemProgram(`WindowsPowerShell\v1.0\powershell.exe`)
					if programErr != nil {
						t.Fatal(programErr)
					}
					script := `$ErrorActionPreference='Stop'; $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); $task=$s.GetFolder('\').GetTask('` + name + `'); $task | Select-Object State,LastTaskResult,LastRunTime | Format-List; $task.Definition.Actions | Select-Object Path | Format-List`
					output, inspectErr := exec.CommandContext(diagnostic, powershell, "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
					t.Fatalf("scheduled process did not start: %v; inspection: %v; %s", ctx.Err(), inspectErr, output)
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
		if err := m.Start(ctx); err != nil {
			t.Fatal(err)
		}
		waitStarted()
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
		if err := m.Restart(ctx); err != nil {
			t.Fatal(err)
		}
		waitStarted()
		if err := m.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	})
	if err := m.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = m.taskState(ctx)
	if err != nil || state != 4 {
		t.Fatalf("task remains after uninstall: state=%d error=%v", state, err)
	}
}

// Native qualification uses only synthetic paths and no Telegram account.
type nativeTaskRunner struct{ t *testing.T }

func (r nativeTaskRunner) Run(ctx context.Context, program string, args ...string) error {
	output, err := exec.CommandContext(ctx, program, args...).CombinedOutput()
	if err != nil && len(output) != 0 {
		r.t.Logf("synthetic %s diagnostic: %s", filepath.Base(program), output)
	}
	return errors.Join(ctx.Err(), err)
}
