package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func TestSettingsRecoveryDisclosesAndResetsSendAccessWithoutBackingItUp(t *testing.T) {
	application := authorizedTextApplication(t, &fakeRuntime{})
	forbidSendingRuntime(t, application)
	ctx := context.Background()
	if err := application.SetFullSend(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := application.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "backup")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "settings.json")
	if err := application.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 {
		t.Fatal("settings backup allowlist changed", len(fields))
	}
	for _, name := range []string{"version", "environment", "test_dc", "retention", "scopes"} {
		if _, exists := fields[name]; !exists {
			t.Fatal("settings backup field missing", name)
		}
	}
	preview, err := application.PreviewRestore(ctx, path)
	if err != nil || preview.Consequences.SendingEnabledAfterRestore {
		t.Fatal("restore preview retained send permission", preview.Consequences, err)
	}
	encoded, err := json.Marshal(preview.Consequences)
	if err != nil {
		t.Fatal(err)
	}
	var consequences map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &consequences); err != nil || string(consequences["sending_enabled_after_restore"]) != "false" {
		t.Fatal("restore preview omitted disabled sending", err)
	}
	if enabled, err := application.FullSend(ctx); err != nil || !enabled {
		t.Fatal("restore preview changed send access", enabled, err)
	}
	if err := application.Restore(ctx, path); err != nil {
		t.Fatal(err)
	}
	if enabled, err := application.FullSend(ctx); err != nil || enabled {
		t.Fatal("restore did not clear send access", enabled, err)
	}
	if full, err := application.FullRead(ctx); err != nil || full {
		t.Fatal("restore did not clear read access", full, err)
	}
}
