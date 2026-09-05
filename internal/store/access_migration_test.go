package store

import (
	"context"
	"github.com/lstpsche/telegram-mcp/migrations"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"
)

func TestFullReadMigrationPreservesRestrictedAuthority(t *testing.T) {
	db := openTestDatabase(t)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0009_" {
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
	if _, err := db.Exec(`INSERT INTO text_grants(peer,authorization_epoch,author,min_id,max_id,read_through,profile,expires_at,eligible,images) VALUES('tgpeer:v1:chat:1','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','tgpeer:v1:user:1',1,20,20,'consented','2026-09-06T00:00:00Z',1,1)`); err != nil {
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
	for range 2 {
		if err := current.Apply(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var enabled, count int
	var after int64
	if err := db.QueryRow("SELECT count(*) FROM full_read_access").Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("upgrade granted full access", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM text_grants WHERE images=1 AND min_id=1 AND max_id=20").Scan(&count); err != nil || count != 1 {
		t.Fatal("upgrade changed exact grant", err)
	}
	if err := db.QueryRow("SELECT revision FROM policy_revision").Scan(&after); err != nil || after != revision {
		t.Fatal("upgrade changed policy revision", err)
	}
}
