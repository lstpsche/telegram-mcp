package diagnostics

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
)

func TestInspectFreshWindowsStateUnderInheritedUserDirectory(t *testing.T) {
	// t.TempDir has the ordinary inherited profile ACL, not a private runtime ACL.
	root := t.TempDir()
	paths, err := daemon.NewPaths(filepath.Join(root, "state"), filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if report.Socket != daemon.SocketAbsent || report.MCP || report.Secrets != "not_checked" {
		t.Fatalf("unexpected fresh report: %+v", report)
	}
	if _, err := os.Stat(paths.StateDir); !os.IsNotExist(err) {
		t.Fatalf("inspection created state: %v", err)
	}
}
