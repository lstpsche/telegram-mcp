package policy

import (
	"context"
	"reflect"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func TestRestoreResetsAuthorityAndNeverRewindsBinding(t *testing.T) {
	r, db, now := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	grant := validGrant(t, *now)
	if err := l.Save(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := l.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	original, err := l.SaveScope(ctx, "", "work", []model.PeerID{grant.Peer})
	if err != nil {
		t.Fatal(err)
	}
	epoch, before, err := l.Binding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO telegram_update_state VALUES(?,1,100,2,1700000000,3)", epoch); err != nil {
		t.Fatal(err)
	}
	if err := l.Audit(ctx, "req_before", "list_scopes", "", 1, false); err != nil {
		t.Fatal(err)
	}
	scopes := []RecoverableScope{{Name: "work", Peers: original.Peers}}
	retention := store.AuditRetention{Days: 7, MaxRecords: 10}
	if err := l.Restore(ctx, scopes, retention); err != nil {
		t.Fatal(err)
	}
	var pts int
	if err := db.QueryRow("SELECT pts FROM telegram_update_state WHERE epoch=?", epoch).Scan(&pts); err != nil || pts != 100 {
		t.Fatal("checkpoint changed", err)
	}
	afterEpoch, after, err := l.Binding(ctx)
	if err != nil || epoch != afterEpoch || after <= before {
		t.Fatal("binding rollback", err)
	}
	full, err := l.FullRead(ctx)
	if err != nil || full {
		t.Fatal("authority restored", err)
	}
	grants, err := l.List(ctx)
	if err != nil || len(grants) != 0 {
		t.Fatal("grants survived", err)
	}
	restored, err := l.Scopes(ctx)
	if err != nil || len(restored) != 1 || restored[0].ID == original.ID || !reflect.DeepEqual(restored[0].Peers, original.Peers) {
		t.Fatal("invalid restored scopes", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM text_audit").Scan(&count); err != nil || count != 1 {
		t.Fatal("audit history changed", err)
	}
	if err := l.Restore(ctx, []RecoverableScope{}, retention); err != nil {
		t.Fatal(err)
	}
	_, before, _ = l.Binding(ctx)
	if err := l.Restore(ctx, []RecoverableScope{}, retention); err != nil {
		t.Fatal(err)
	}
	_, after, _ = l.Binding(ctx)
	if after <= before {
		t.Fatal("empty restore reused binding")
	}
}

func TestRestoreFailureRollsBackScopesAuthorityAndSettings(t *testing.T) {
	r, db, _ := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	original, err := l.SaveScope(ctx, "", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	_, before, _ := l.Binding(ctx)
	if _, err := db.Exec(`CREATE TRIGGER reject_restore BEFORE INSERT ON named_scope_peers BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := l.Restore(ctx, []RecoverableScope{{Name: "new", Peers: []model.PeerID{peer(t, model.PeerKindChat, 1)}}}, store.AuditRetention{Days: 1, MaxRecords: 1}); err == nil {
		t.Fatal("failed restore succeeded")
	}
	scopes, err := l.Scopes(ctx)
	if err != nil || len(scopes) != 1 || scopes[0].ID != original.ID {
		t.Fatal("scope rollback failed", err)
	}
	full, err := l.FullRead(ctx)
	if err != nil || !full {
		t.Fatal("authority rollback failed", err)
	}
	_, after, _ := l.Binding(ctx)
	if after != before {
		t.Fatal("revision escaped rollback")
	}
	var days int
	if err := db.QueryRow("SELECT days FROM audit_retention").Scan(&days); err != nil || days != 30 {
		t.Fatal("settings escaped rollback", err)
	}
}

func TestAuditRetentionAgeCountAndFailureAreAtomic(t *testing.T) {
	r, db, now := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	if _, err := db.Exec("UPDATE audit_retention SET days=1,max_records=2"); err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-09-04T11:59:59.999999999Z", "2026-09-04T12:00:00Z", "2026-09-04T12:00:00.000000001Z"} {
		if _, err := db.Exec("INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_old','list_scopes','success','',0,0,?)", date); err != nil {
			t.Fatal(err)
		}
	}
	// First prune preserves both rows at/after the exact second boundary.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := store.PruneAudit(ctx, tx, *now)
	if err != nil || deleted != 1 {
		t.Fatal("cutoff boundary", deleted, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := l.Audit(ctx, "req_new", "catch_up", "", 1, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM text_audit").Scan(&count); err != nil || count != 2 {
		t.Fatal("count limit", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_prune BEFORE DELETE ON text_audit BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := l.Audit(ctx, "req_rejected", "catch_up", "", 1, false); err == nil {
		t.Fatal("pruning error hidden")
	}
	if err := db.QueryRow("SELECT count(*) FROM text_audit WHERE request_id='req_rejected'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed audit committed", err)
	}
}
