package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

type fakeMaintenance struct {
	controller
	called       bool
	previewed    bool
	restored     bool
	retention    *store.AuditRetention
	apply, purge bool
}

func (f *fakeMaintenance) Backup(context.Context, string) error { f.called = true; return nil }
func (f *fakeMaintenance) PreviewRestore(context.Context, string) (app.RestorePreview, error) {
	f.called = true
	f.previewed = true
	return app.RestorePreview{Consequences: app.RestoreConsequences{AccessModeAfterRestore: "restricted"}}, nil
}
func (f *fakeMaintenance) Restore(context.Context, string) error {
	f.called = true
	f.restored = true
	return nil
}
func (f *fakeMaintenance) AuditMaintenance(_ context.Context, r *store.AuditRetention, apply, purge bool) (store.AuditStatus, error) {
	f.called = true
	f.retention = r
	f.apply = apply
	f.purge = purge
	return store.AuditStatus{Retention: store.AuditRetention{Days: 30, MaxRecords: 10000}}, nil
}

func TestMaintenanceRequiresExplicitDestructiveFlags(t *testing.T) {
	for _, args := range [][]string{{"restore", "--file", "/backup"}, {"restore", "--file", "/backup", "--replace-scopes"}, {"restore", "--dry-run", "/backup"}, {"restore", "--file", "/backup", "--dry-run"}, {"restore", "--dry-run", "--file", "/backup", "--replace-scopes", "--reset-access"}, {"audit", "purge", "--all"}, {"audit", "prune"}, {"audit", "retention", "--days", "30", "--max-records", "100"}, {"audit", "retention", "--days", "0", "--max-records", "100", "--apply"}, {"audit", "retention", "--days", "01", "--max-records", "100", "--apply"}, {"backup", "--file", "/backup", "extra"}} {
		var out, stderr bytes.Buffer
		f := &fakeMaintenance{}
		if code := runMaintenanceCommand(context.Background(), args, &out, &stderr, f); code != 2 || f.called || out.Len() != 0 {
			t.Fatal("invalid command invoked maintenance", args, code)
		}
	}
}

func TestMaintenanceCommandsAreRoutedAndReportSuccess(t *testing.T) {
	for _, args := range [][]string{{"backup", "--file", "/backup"}, {"restore", "--dry-run", "--file", "/backup"}, {"restore", "--file", "/backup", "--replace-scopes", "--reset-access"}, {"audit"}, {"audit", "prune", "--confirm"}, {"audit", "purge", "--all", "--confirm"}, {"audit", "retention", "--days", "7", "--max-records", "50", "--apply"}} {
		var out, stderr bytes.Buffer
		f := &fakeMaintenance{}
		factory := func() (controller, error) { return f, nil }
		if code := runContext(context.Background(), args, &out, &stderr, factory, nil); code != 0 || !f.called || stderr.Len() != 0 {
			t.Fatal("command failed", args, code, stderr.String())
		}
	}
}

func TestRestoreDryRunDoesNotRouteToRestore(t *testing.T) {
	var out, stderr bytes.Buffer
	f := &fakeMaintenance{}
	factory := func() (controller, error) { return f, nil }
	if code := runContext(context.Background(), []string{"restore", "--dry-run", "--file", "/backup"}, &out, &stderr, factory, nil); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !f.previewed || f.restored {
		t.Fatalf("previewed=%v restored=%v", f.previewed, f.restored)
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("invalid preview JSON: %q", out.String())
	}
}

// Embedding a nil interface makes any secret access fail immediately.
type inaccessibleSecrets struct{ tgaccount.SecretStore }

func TestRestoreDryRunWithRealMetadata(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths, err := daemon.NewPaths(filepath.Join(root, "state"), filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo, err := store.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveConfig(ctx, store.AccountConfig{APIID: 12345, Environment: "test", TestDC: 2, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordAuthorization(ctx, "synthetic_authorization_epoch_12345", nil, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	control, err := app.New(paths, inaccessibleSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(paths.StateDir, "backup.json")
	if err := privatefs.WriteFile(backup, []byte(`{"version":1,"environment":"test","test_dc":2,"retention":{"days":90,"max_records":500},"scopes":[{"name":"proposed","peers":[]}]}`), false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := runContext(ctx, []string{"restore", "--dry-run", "--file", backup}, &out, &stderr, func() (controller, error) { return control, nil }, nil)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var preview app.RestorePreview
	if err := json.Unmarshal(out.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Current.Scopes) != 0 || len(preview.Proposed.Scopes) != 1 || preview.Proposed.Retention.Days != 90 || preview.Consequences.ScopeIDsRegenerated != 1 {
		t.Fatalf("unexpected preview: %s", out.String())
	}
	t.Logf("preview JSON: %s", out.String())
	after, err := os.ReadFile(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed database bytes")
	}
	out.Reset()
	stderr.Reset()
	code = runContext(ctx, []string{"restore", "--dry-run", "--file", backup + ".missing"}, &out, &stderr, func() (controller, error) { return control, nil }, nil)
	if code != 1 || out.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("failed preview emitted a result: code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
}
