package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/store"
)

type fakeMaintenance struct {
	controller
	called       bool
	retention    *store.AuditRetention
	apply, purge bool
}

func (f *fakeMaintenance) Backup(context.Context, string) error  { f.called = true; return nil }
func (f *fakeMaintenance) Restore(context.Context, string) error { f.called = true; return nil }
func (f *fakeMaintenance) AuditMaintenance(_ context.Context, r *store.AuditRetention, apply, purge bool) (store.AuditStatus, error) {
	f.called = true
	f.retention = r
	f.apply = apply
	f.purge = purge
	return store.AuditStatus{Retention: store.AuditRetention{Days: 30, MaxRecords: 10000}}, nil
}

func TestMaintenanceRequiresExplicitDestructiveFlags(t *testing.T) {
	for _, args := range [][]string{{"restore", "--file", "/backup"}, {"restore", "--file", "/backup", "--replace-scopes"}, {"audit", "purge", "--all"}, {"audit", "prune"}, {"audit", "retention", "--days", "30", "--max-records", "100"}, {"audit", "retention", "--days", "0", "--max-records", "100", "--apply"}, {"audit", "retention", "--days", "01", "--max-records", "100", "--apply"}, {"backup", "--file", "/backup", "extra"}} {
		var out, stderr bytes.Buffer
		f := &fakeMaintenance{}
		if code := runMaintenanceCommand(context.Background(), args, &out, &stderr, f); code != 2 || f.called || out.Len() != 0 {
			t.Fatal("invalid command invoked maintenance", args, code)
		}
	}
}

func TestMaintenanceCommandsAreRoutedAndReportSuccess(t *testing.T) {
	for _, args := range [][]string{{"backup", "--file", "/backup"}, {"restore", "--file", "/backup", "--replace-scopes", "--reset-access"}, {"audit"}, {"audit", "prune", "--confirm"}, {"audit", "purge", "--all", "--confirm"}, {"audit", "retention", "--days", "7", "--max-records", "50", "--apply"}} {
		var out, stderr bytes.Buffer
		f := &fakeMaintenance{}
		factory := func() (controller, error) { return f, nil }
		if code := runContext(context.Background(), args, &out, &stderr, factory, nil); code != 0 || !f.called || stderr.Len() != 0 {
			t.Fatal("command failed", args, code, stderr.String())
		}
	}
}
