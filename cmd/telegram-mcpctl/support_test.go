package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/mcpserver"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

func TestSupportRejectsArgumentsBeforeInitializingAnything(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"doctor", "secret"}, {"agent-config", "secret"}, {"service"}, {"service", "install"}, {"service", "install", "--bin-dir", "/bin", "secret"}, {"service", "stop", "secret"}, {"service", "kill"}} {
		var out, errout bytes.Buffer
		code := runSupportCommand(context.Background(), args, &out, &errout, func() (support, error) {
			t.Fatal("factory invoked for invalid arguments")
			return support{}, nil
		})
		if code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "secret") {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), errout.String())
		}
	}
	// Exercise the real dispatcher with invalid support arguments. Both account
	// initialization paths must stay unreachable even before support validation.
	var out, errout bytes.Buffer
	if code := runContext(context.Background(), []string{"doctor", "secret"}, &out, &errout,
		func() (controller, error) {
			t.Fatal("account controller opened")
			return nil, nil
		},
		func() (terminal, error) {
			t.Fatal("terminal opened")
			return nil, nil
		}); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestAgentConfigurationPreservesExactExecutablePath(t *testing.T) {
	t.Parallel()
	path := "/Users/example/Apps/Telegram & MCP/版本/telegram-mcp"
	manager := &fakeService{installed: &service.Config{Relay: path}}
	var out, errout bytes.Buffer
	code := runSupportCommand(context.Background(), []string{"agent-config"}, &out, &errout, func() (support, error) {
		return support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
			t.Fatal("runtime inspected for config")
			return diagnostics.Report{}, nil
		}}, nil
	})
	var actual map[string]map[string]map[string]string
	if err := json.Unmarshal(out.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if code != 0 || errout.Len() != 0 || actual["mcpServers"]["telegram"]["command"] != path {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errout.String())
	}
}

func TestDoctorSeparatesConnectivityFromContentReadiness(t *testing.T) {
	t.Parallel()
	state := daemon.StateReauthRequired
	reads := false
	for _, tc := range []struct {
		name      string
		installed bool
		mcp       bool
		code      int
		action    string
	}{
		{"absent", false, false, 1, "install_service"},
		{"stopped", true, false, 1, "check_service_startup"},
		{"unconfigured", true, true, 0, "inspect_account_readiness"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &fakeService{}
			if tc.installed {
				manager.installed = &service.Config{Relay: "/safe/telegram-mcp"}
			}
			var out, errout bytes.Buffer
			report := diagnostics.Report{Socket: daemon.SocketAbsent, Secrets: "not_checked", Metadata: "filesystem_only"}
			if tc.mcp {
				report.Socket = daemon.SocketLive
				report.MCP = true
				report.AccountState = &state
				report.MessageReads = &reads
			}
			code := runSupportCommand(context.Background(), []string{"doctor"}, &out, &errout, func() (support, error) {
				return support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) { return report, nil }}, nil
			})
			var result doctorResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if code != tc.code || result.NextAction != tc.action || !reflect.DeepEqual(result.Runtime, report) || errout.Len() != 0 {
				t.Fatalf("code=%d result=%+v err=%q", code, result, errout.String())
			}
		})
	}
}

func TestSupportErrorsNeverEmitPartialReportsOrRawCauses(t *testing.T) {
	t.Parallel()
	hostile := errors.New("sensitive remote text")
	for _, command := range [][]string{{"doctor"}, {"agent-config"}, {"service", "start"}} {
		var out, errout bytes.Buffer
		manager := &fakeService{err: hostile}
		code := runSupportCommand(context.Background(), command, &out, &errout, func() (support, error) { return support{service: manager}, nil })
		if code != 1 || out.Len() != 0 || strings.Contains(errout.String(), hostile.Error()) || strings.Count(errout.String(), "\n") != 1 {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), errout.String())
		}
	}
	var out, errout bytes.Buffer
	code := runSupportCommand(context.Background(), []string{"doctor"}, &out, &errout, func() (support, error) {
		return support{service: &fakeService{installed: &service.Config{}}, inspect: func(context.Context) (diagnostics.Report, error) { return diagnostics.Report{MCP: true}, hostile }}, nil
	})
	if code != 1 || out.Len() != 0 || strings.Contains(errout.String(), hostile.Error()) {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errout.String())
	}
}

func TestServiceDispatchKeepsMutationExplicit(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"install", "start", "stop", "restart", "uninstall"} {
		manager := &fakeService{}
		args := []string{"service", action}
		if action == "install" {
			args = append(args, "--bin-dir", "/chosen/artifacts")
		}
		var out, errout bytes.Buffer
		code := runSupportCommand(context.Background(), args, &out, &errout, func() (support, error) { return support{service: manager}, nil })
		if code != 0 || manager.action != action || errout.Len() != 0 {
			t.Fatalf("action=%s code=%d got=%s err=%q", action, code, manager.action, errout.String())
		}
		if action == "install" && manager.binDir != "/chosen/artifacts" {
			t.Fatal("binary directory changed")
		}
	}
}

type fakeService struct {
	installed      *service.Config
	err            error
	action, binDir string
}

func (f *fakeService) Inspect(context.Context) (*service.Config, error) { return f.installed, f.err }
func (f *fakeService) Install(_ context.Context, dir string) (service.Config, error) {
	f.action = "install"
	f.binDir = dir
	return service.Config{}, f.err
}
func (f *fakeService) Start(context.Context) error {
	f.action = "start"
	return f.err
}
func (f *fakeService) Stop(context.Context) error {
	f.action = "stop"
	return f.err
}
func (f *fakeService) Restart(context.Context) error {
	f.action = "restart"
	return f.err
}
func (f *fakeService) Uninstall(context.Context) error {
	f.action = "uninstall"
	return f.err
}

func TestLocalSupportWorkflowWithSyntheticMCP(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("user service excludes root")
	}
	tempDir := "/tmp"
	if runtime.GOOS == "windows" {
		tempDir = os.TempDir()
	}
	root, err := os.MkdirTemp(tempDir, "tmcp-support-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	home, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		home = filepath.Join(home, "private")
		if err := privatefs.EnsureDirectory(home); err != nil {
			t.Fatal(err)
		}
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	bin := filepath.Join(home, "artifacts with spaces")
	if err := privatefs.EnsureDirectory(bin); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"telegram-mcp", "telegram-mcpd", "telegram-mcpctl"} {
		if err := privatefs.WriteFile(filepath.Join(bin, name+suffix), []byte("synthetic artifact"), false); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(filepath.Join(bin, name), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	runner := &workflowRunner{}
	manager, err := service.New(home, os.Geteuid(), runner)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := daemon.NewPaths(filepath.Join(home, "Library", "Application Support", "Telegram MCP"), filepath.Join(home, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	factory := func() (support, error) {
		return support{service: manager, inspect: func(ctx context.Context) (diagnostics.Report, error) { return diagnostics.Inspect(ctx, paths) }}, nil
	}
	invoke := func(want int, args ...string) []byte {
		t.Helper()
		var out, errout bytes.Buffer
		if code := runSupportCommand(context.Background(), args, &out, &errout, factory); code != want || errout.Len() != 0 {
			t.Fatalf("command=%v code=%d stderr=%q", args, code, errout.String())
		}
		return out.Bytes()
	}
	invoke(0, "service", "install", "--bin-dir", bin)
	if err := privatefs.EnsureDirectory(paths.StateDir); err != nil {
		t.Fatal(err)
	}
	metadata := []byte("deliberately invalid SQLite to detect accidental open")
	if err := os.WriteFile(paths.Database, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	invoke(1, "doctor")
	invoke(0, "service", "start")
	socket, err := daemon.BindSocket(paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	server := mcpserver.New(func() daemon.Snapshot { return daemon.Snapshot{State: daemon.StateReauthRequired} }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer socket.Close()
	done := make(chan error, 1)
	go func() {
		done <- socket.Serve(ctx, func(ctx context.Context, connection net.Conn) { mcpserver.Serve(ctx, server, connection) })
	}()
	output := invoke(0, "doctor")
	var report doctorResult
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if report.Installation != "valid" || !report.Runtime.MCP || report.Runtime.AccountState == nil || *report.Runtime.AccountState != daemon.StateReauthRequired {
		t.Fatalf("wrong report: %+v", report)
	}
	var config map[string]map[string]map[string]string
	if err := json.Unmarshal(invoke(0, "agent-config"), &config); err != nil {
		t.Fatal(err)
	}
	if config["mcpServers"]["telegram"]["command"] != filepath.Join(bin, "telegram-mcp"+suffix) {
		t.Fatal("configuration changed installed path")
	}
	invoke(0, "service", "stop")
	cancel()
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("synthetic server did not stop")
	}
	invoke(0, "service", "uninstall")
	if data, err := os.ReadFile(paths.Database); err != nil || !bytes.Equal(data, metadata) {
		t.Fatal("support commands changed metadata")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(paths.Database + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("support commands opened SQLite")
		}
	}
	if _, err := os.Stat(filepath.Join(bin, "telegram-mcpd"+suffix)); err != nil {
		t.Fatal("uninstall removed artifacts")
	}
}

type workflowRunner struct{ loaded, registered bool }
type workflowExit int

func (e workflowExit) Error() string { return "synthetic process status" }
func (e workflowExit) ExitCode() int { return int(e) }
func (r *workflowRunner) Run(ctx context.Context, program string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "Get-ScheduledTask") {
		if !r.registered {
			return workflowExit(4)
		}
		if r.loaded {
			return nil
		}
		return workflowExit(3)
	}
	if program == "/usr/bin/systemctl" || strings.EqualFold(filepath.Base(program), "schtasks.exe") {
		switch {
		case strings.Contains(joined, "is-enabled"):
			if r.registered {
				return nil
			}
			return workflowExit(1)
		case strings.Contains(joined, "is-active"):
			if r.loaded {
				return nil
			}
			return workflowExit(3)
		case strings.Contains(joined, "/Create") || strings.Contains(joined, " enable "):
			r.registered = true
		case strings.Contains(joined, "/Run") || strings.Contains(joined, " start "):
			r.loaded = true
		case strings.Contains(joined, "/End") || strings.Contains(joined, " stop "):
			r.loaded = false
		case strings.Contains(joined, "/Delete") || strings.Contains(joined, " disable "):
			r.registered = false
		case strings.Contains(joined, "daemon-reload"):
		default:
			return errors.New("unexpected synthetic action")
		}
		return nil
	}
	if program != "/bin/launchctl" || len(args) < 2 {
		return errors.New("unexpected synthetic command")
	}
	switch args[0] {
	case "print":
		if strings.Count(args[1], "/") == 2 && !r.loaded {
			return workflowExit(113)
		}
	case "bootstrap":
		r.loaded = true
	case "bootout":
		r.loaded = false
	default:
		return errors.New("unexpected synthetic action")
	}
	return nil
}
