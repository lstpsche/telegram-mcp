package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
)

func TestTranscriptionMigrationPreservesVoiceAuthorityAuditAndRetention(t *testing.T) {
	database := openTestDatabase(t)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0014_" {
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
	if _, err := database.Exec(`INSERT INTO text_grants(peer,authorization_epoch,author,min_id,max_id,read_through,profile,expires_at,eligible,voice_notes) VALUES('tgpeer:v1:chat:1','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','tgpeer:v1:user:1',1,20,20,'consented','2026-09-06T00:00:00Z',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_old','open_voice_note','success','',1,0,'2026-09-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE audit_retention SET days=14,max_records=50`); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := database.QueryRow(`SELECT revision FROM policy_revision`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	current, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	var voice bool
	var newRevision int64
	var days, records, count int
	if err := database.QueryRow(`SELECT voice_notes FROM text_grants`).Scan(&voice); err != nil || !voice {
		t.Fatal("voice authority changed", err)
	}
	if err := database.QueryRow(`SELECT revision FROM policy_revision`).Scan(&newRevision); err != nil || revision != newRevision {
		t.Fatal("authority binding changed", err)
	}
	if err := database.QueryRow(`SELECT days,max_records FROM audit_retention`).Scan(&days, &records); err != nil || days != 14 || records != 50 {
		t.Fatal("retention changed", err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM text_audit WHERE id=1 AND request_id='req_old' AND operation='open_voice_note' AND item_count=1`).Scan(&count); err != nil || count != 1 {
		t.Fatal("existing audit changed", err)
	}
	for _, category := range []string{"transcription_pending", "transcription_unavailable", "transcription_failed"} {
		if _, err := database.Exec(`INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_transcript','transcribe_voice_note','failure',?,0,0,'2026-09-05T00:00:00Z')`, category); err != nil {
			t.Fatal("transcription audit rejected", err)
		}
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
}
