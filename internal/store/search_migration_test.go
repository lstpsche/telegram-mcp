package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
)

func TestSearchMigrationPreservesExistingAuditAndGrants(t *testing.T) {
	database := openTestDatabase(t)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0005_" {
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
	if err := old.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_old','list_messages','success','',1,0,'2026-09-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO text_grants(peer,authorization_epoch,author,min_id,max_id,read_through,profile,expires_at,eligible) VALUES('tgpeer:v1:chat:1','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','tgpeer:v1:user:1',1,20,20,'consented','2026-09-06T00:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	current, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	var id, count int
	var request string
	if err := database.QueryRow("SELECT id,request_id,item_count FROM text_audit").Scan(&id, &request, &count); err != nil || id != 1 || request != "req_old" || count != 1 {
		t.Fatal("audit changed during migration", err)
	}
	if err := database.QueryRow("SELECT count(*) FROM text_grants").Scan(&count); err != nil || count != 1 {
		t.Fatal("grant lost", err)
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal("migration not repeatable", err)
	}
}
