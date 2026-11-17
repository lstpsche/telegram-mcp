package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Run only in a disposable non-root Linux login with a running systemd user
// manager and fresh configuration/cache directories. The input directory must
// contain locally built telegram-mcp and telegram-mcpd programs.
func TestNativeSystemdRelayStartup(t *testing.T) {
	if os.Getenv("TELEGRAM_MCP_NATIVE_SYSTEMD_TEST") != "1" {
		t.Skip("requires a disposable systemd user session")
	}
	source := os.Getenv("TELEGRAM_MCP_NATIVE_SYSTEMD_BIN_DIR")
	if !filepath.IsAbs(source) {
		t.Fatal("native test requires an absolute binary input directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	paths, err := daemon.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.StateDir, paths.RuntimeDir} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("native test requires unused account state and runtime directories")
		}
	}
	manager, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.systemdState(ctx)
	if err != nil || !systemdAbsent(state) {
		t.Fatalf("native test requires no existing user unit: %v", err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binaries := filepath.Join(base, "binaries café %")
	if err := privatefs.EnsureDirectory(binaries); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"telegram-mcp", "telegram-mcpd"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := privatefs.WriteExecutable(filepath.Join(binaries, name), data); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := os.Lstat(manager.recordPath()); err == nil {
			if err := manager.Stop(cleanup); err != nil {
				t.Error(err)
				return
			}
			if err := manager.Uninstall(cleanup); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := manager.Install(ctx, binaries); err != nil {
		t.Fatal(err)
	}

	connect := func() (*mcp.ClientSession, error) {
		startup, stop := context.WithTimeout(ctx, 10*time.Second)
		command := exec.CommandContext(ctx, filepath.Join(binaries, "telegram-mcp"))
		client := mcp.NewClient(&mcp.Implementation{Name: "native-service-check", Version: "1"}, nil)
		session, err := client.Connect(startup, &mcp.CommandTransport{Command: command}, nil)
		if err != nil {
			stop()
			return nil, err
		}
		t.Cleanup(func() {
			_ = session.Close()
			stop()
		})
		return session, nil
	}

	const clients = 4
	sessions := make(chan *mcp.ClientSession, clients)
	failures := make(chan error, clients)
	var waiting sync.WaitGroup
	for range clients {
		waiting.Go(func() {
			session, err := connect()
			if err != nil {
				failures <- err
				return
			}
			sessions <- session
		})
	}
	waiting.Wait()
	close(failures)
	close(sessions)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	pid := nativeSystemdPID(t, ctx, manager)
	for session := range sessions {
		tools, err := session.ListTools(ctx, nil)
		if err != nil || len(tools.Tools) == 0 {
			t.Fatalf("native tool discovery: %v", err)
		}
		status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{}})
		if err != nil || status.IsError {
			t.Fatalf("native status: %v", err)
		}
	}
	report, err := diagnostics.Inspect(ctx, paths)
	if err != nil || !report.MCP || report.AccountState == nil || *report.AccountState != daemon.StateReauthRequired {
		t.Fatalf("unconfigured native status: %+v %v", report, err)
	}
	if nativeSystemdPID(t, ctx, manager) != pid {
		t.Fatal("concurrent clients replaced the shared runtime")
	}

	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.WithSession(ctx, func(s *Session) error {
		enabled, err := s.Enabled(ctx)
		if err != nil || enabled {
			return errors.New("stop did not retain disabled enablement")
		}
		_, err = s.RequestStart(ctx, time.Now())
		if !errors.Is(err, ErrDisabled) {
			return errors.New("automatic startup did not respect persistent stop")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.DialSocket(ctx, paths.Socket); !errors.Is(err, daemon.ErrDaemonUnavailable) {
		t.Fatalf("stopped transport: %v", err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	resumed, err := connect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if nativeSystemdPID(t, ctx, manager) == pid {
		t.Fatal("explicit resume did not replace the stopped process")
	}
}

func nativeSystemdPID(t *testing.T, ctx context.Context, manager *Manager) int {
	t.Helper()
	output, err := manager.runner.(Queryer).Query(ctx, "/usr/bin/systemctl", "--user", "show", "--property=MainPID", "--value", Label+".service")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 0 {
		t.Fatal("native service has no running process")
	}
	return pid
}
