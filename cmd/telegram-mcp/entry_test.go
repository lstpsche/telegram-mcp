package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

func TestEntryInformationReportsPhysicallyExecutingProgram(t *testing.T) {
	var output, diagnostics bytes.Buffer
	handled, code := runEntryInfo([]string{"--entry-info"}, &output, &diagnostics)
	var info distribution.EntryInfo
	if err := json.Unmarshal(output.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil || !handled || code != 0 || diagnostics.Len() != 0 || info.Protocol != distribution.EntryProtocol || info.Executable != executable {
		t.Fatalf("entry information: %+v, handled=%t code=%d error=%v", info, handled, code, err)
	}
	output.Reset()
	handled, code = runEntryInfo([]string{"--entry-info", "private-input"}, &output, &diagnostics)
	if !handled || code != 2 || output.Len() != 0 || strings.Contains(diagnostics.String(), "private-input") {
		t.Fatal("invalid entry arguments were not safely rejected")
	}
}

func TestEntryDeadlineEnvironmentReplacesInheritedValues(t *testing.T) {
	deadline := time.UnixMilli(123456)
	env := entryEnvironment([]string{"OTHER=kept", "TELEGRAM_MCP_STARTUP_DEADLINE_UNIX_MS=old", "TELEGRAM_MCP_STARTUP_DEADLINE_UNIX_MS=another"}, deadline)
	if strings.Join(env, "\n") != "OTHER=kept\nTELEGRAM_MCP_STARTUP_DEADLINE_UNIX_MS=123456" {
		t.Fatal("invalid inherited deadline environment")
	}
}

func TestSelectedEntryUsesOnlyCanonicalManagedRegistration(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "private")
	directory := filepath.Join(root, "versions", "1.5.0")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(root, distribution.Binary("telegram-mcp"))
	selected := filepath.Join(directory, distribution.Binary("telegram-mcp"))
	for _, path := range []string{stable, selected} {
		if err := privatefs.WriteExecutable(path, []byte("synthetic executable")); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS == "windows" {
		alias := filepath.Join(root, strings.ToUpper(filepath.Base(stable)))
		match, err := isManagedEntry(alias, root)
		if err != nil || !match {
			t.Fatal("rejected stable executable path casing", err)
		}
	}
	for _, tc := range []struct {
		name   string
		config *service.Config
		valid  bool
	}{
		{"selected", &service.Config{BinDir: directory, Relay: selected}, true},
		{"absent", nil, false},
		{"unmanaged", &service.Config{BinDir: filepath.Join(root, "manual"), Relay: selected}, false},
		{"recursive", &service.Config{BinDir: directory, Relay: stable}, false},
		{"different relay", &service.Config{BinDir: directory, Relay: selected + ".other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := selectedEntry(context.Background(), stable, root, func(context.Context) (*service.Config, error) { return tc.config, nil })
			if (err == nil) != tc.valid || (tc.valid && path != selected) {
				t.Fatalf("selection: %q %v", path, err)
			}
		})
	}
}
