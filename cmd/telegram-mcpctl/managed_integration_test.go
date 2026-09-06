package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

// Exercise actual control executables against a synthetic service registration.
// The OS runner is fake, and the isolated profile contains no account state.
func TestStableControlDispatchUsesRegisteredVersion(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("per-user installation excludes root")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var programs [][]byte
	for _, version := range []string{"0.2.0", "0.3.0"} {
		path := filepath.Join(parent, "control-"+version+".exe")
		command := exec.Command("go", "build", "-o", path, "-ldflags", "-X github.com/lstpsche/telegram-mcp/internal/buildinfo.Version="+version+" -X github.com/lstpsche/telegram-mcp/internal/buildinfo.Commit="+strings.Repeat("a", 40), ".")
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build synthetic control: %v: %s", err, data)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		programs = append(programs, data)
	}
	home := filepath.Join(parent, "home")
	if err := privatefs.EnsureDirectory(home); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	root, err := distribution.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "versions", "0.3.0")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(root, distribution.Binary("telegram-mcpctl"))
	if err := privatefs.WriteExecutable(stable, programs[0]); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"telegram-mcpctl", "telegram-mcp", "telegram-mcpd"} {
		if err := privatefs.WriteExecutable(filepath.Join(directory, distribution.Binary(name)), programs[1]); err != nil {
			t.Fatal(err)
		}
	}
	runner := &workflowRunner{}
	manager, err := service.New(home, os.Geteuid(), runner)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := manager.Install(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after writing the registration but before enabling it.
	runner.registered = false
	local := support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) { return diagnostics.Report{MCP: true}, nil }}
	if err := activateRelease(context.Background(), local, &installed, directory); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(stable, "--version")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch: %v: %s", err, data)
	}
	if !strings.Contains(string(data), "version=0.3.0") || strings.Contains(string(data), "version=0.2.0") {
		t.Fatal("did not use registered control", string(data))
	}
}
