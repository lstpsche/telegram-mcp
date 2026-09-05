package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
)

func TestCatchUpMigrationPreservesAuditAndRevision(t *testing.T) {
	db := openTestDatabase(t)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0010_" {
			continue
		}
		data, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		prior[entry.Name()] = &fstest.MapFile{Data: data}
	}
	old, err := NewMigrator(prior, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := old.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	insert := "INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_audit',?,'success','',1,0,'2026-09-05T00:00:00Z')"
	if _, err := db.Exec(insert, "search_messages"); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.QueryRow("SELECT revision FROM policy_revision").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	current, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var currentRevision int64
	if err := db.QueryRow("SELECT revision FROM policy_revision").Scan(&currentRevision); err != nil || revision != currentRevision {
		t.Fatal("authority revision changed", err)
	}
	var operation string
	var id, count int
	if err := db.QueryRow("SELECT id,operation,item_count FROM text_audit").Scan(&id, &operation, &count); err != nil || id != 1 || operation != "search_messages" || count != 1 {
		t.Fatal("existing audit changed", err)
	}
	if _, err := db.Exec(insert, "catch_up"); err != nil {
		t.Fatal("catch-up audit rejected", err)
	}
	if _, err := db.Exec(insert, "invalid"); err == nil {
		t.Fatal("unknown operation accepted")
	}
	if err := current.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
}
