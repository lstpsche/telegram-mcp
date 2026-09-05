package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
)

func TestImageMigrationPreservesAuthorityAndDeniesExistingGrants(t *testing.T) {
	database := openTestDatabase(t)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0007_" {
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
	if _, err := database.Exec(`INSERT INTO text_grants(peer,authorization_epoch,author,min_id,max_id,read_through,profile,expires_at,eligible) VALUES('tgpeer:v1:chat:1','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','tgpeer:v1:user:1',1,20,20,'consented','2026-09-06T00:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_old','list_scopes','success','',1,0,'2026-09-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := database.QueryRow("SELECT revision FROM policy_revision").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	current, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	var images bool
	if err := database.QueryRow("SELECT images FROM text_grants").Scan(&images); err != nil || images {
		t.Fatal("existing grant gained image permission", err)
	}
	var currentRevision int64
	if err := database.QueryRow("SELECT revision FROM policy_revision").Scan(&currentRevision); err != nil || currentRevision != revision {
		t.Fatal("migration changed authority revision", err)
	}
	var request, operation string
	var id, count int
	if err := database.QueryRow("SELECT id,request_id,operation,item_count FROM text_audit").Scan(&id, &request, &operation, &count); err != nil || id != 1 || request != "req_old" || operation != "list_scopes" || count != 1 {
		t.Fatal("migration changed existing audit", err)
	}
	if _, err := database.Exec("UPDATE text_grants SET images = 1"); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT revision FROM policy_revision").Scan(&currentRevision); err != nil || currentRevision <= revision {
		t.Fatal("image permission update lost invalidation trigger", err)
	}
	if _, err := database.Exec("UPDATE text_grants SET images = 2"); err == nil {
		t.Fatal("nonboolean image permission accepted")
	}
	if _, err := database.Exec(`INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_image','open_image','success','',1,0,'2026-09-05T00:00:00Z')`); err != nil {
		t.Fatal("image audit rejected", err)
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal("migration reapplication failed", err)
	}
}
