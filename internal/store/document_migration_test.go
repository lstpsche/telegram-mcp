package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
)

func TestDocumentMigrationPreservesAuthorityAuditAndRetention(t *testing.T) {
	database := openTestDatabase(t)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0012_" {
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
	if _, err := database.Exec("UPDATE text_grants SET images = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE audit_retention SET days=14,max_records=50"); err != nil {
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
	var preservedImages bool
	var days, records, indexes int
	if err := database.QueryRow("SELECT images FROM text_grants").Scan(&preservedImages); err != nil || !preservedImages {
		t.Fatal("image authority changed", err)
	}
	if err := database.QueryRow("SELECT days,max_records FROM audit_retention").Scan(&days, &records); err != nil || days != 14 || records != 50 {
		t.Fatal("retention changed", err)
	}
	if err := database.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name='text_audit_recorded_at'").Scan(&indexes); err != nil || indexes != 1 {
		t.Fatal("retention index lost", err)
	}
	var documents bool
	if err := database.QueryRow("SELECT documents FROM text_grants").Scan(&documents); err != nil || documents {
		t.Fatal("existing grant gained document permission", err)
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
	if _, err := database.Exec("UPDATE text_grants SET documents = 1"); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT revision FROM policy_revision").Scan(&currentRevision); err != nil || currentRevision <= revision {
		t.Fatal("document permission update lost invalidation trigger", err)
	}
	if _, err := database.Exec("UPDATE text_grants SET documents = 2"); err == nil {
		t.Fatal("nonboolean document permission accepted")
	}
	if _, err := database.Exec(`INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_document','open_document','success','',1,0,'2026-09-05T00:00:00Z')`); err != nil {
		t.Fatal("document audit rejected", err)
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal("migration reapplication failed", err)
	}
}
