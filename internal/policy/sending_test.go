package policy

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func seedRetainedSendFacts(t *testing.T, db *sql.DB, epoch string, now time.Time) {
	t.Helper()
	id, err := model.NewOperationID(now, bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	date := now.Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO outgoing_operations(operation_id,authorization_epoch,peer,payload_digest,random_id,state,failure_code,created_at,updated_at,retain_until) VALUES(?,?,'tgpeer:v1:user:99',zeroblob(32),7,'uncertain','send_effect_uncertain',?,?,?)`, id, epoch, date, date, now.Add(model.SendRetention).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO telegram_rpc_waits(environment,account_id,method,scope_peer,not_before) VALUES('production',123,'messages.sendMessage','',?)`, now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}
func assertRetainedSendFacts(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"outgoing_operations", "telegram_rpc_waits"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatal("retained protocol fact changed", table, count, err)
		}
	}
}

func TestFullSendIsIndependentDefaultDenyAuthority(t *testing.T) {
	r, db, now := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	enabled, err := l.FullSend(ctx)
	if err != nil || enabled {
		t.Fatal("default sending enabled", err)
	}
	_, before, _ := l.Binding(ctx)
	if err := l.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	enabled, err = l.FullSend(ctx)
	if err != nil || enabled {
		t.Fatal("reading granted sending", err)
	}
	if err := l.SetFullRead(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := l.SetFullSend(ctx, true); err != nil {
		t.Fatal(err)
	}
	enabled, err = l.FullSend(ctx)
	if err != nil || !enabled {
		t.Fatal("send opt-in unavailable", err)
	}
	fullRead, err := l.FullRead(ctx)
	if err != nil || fullRead {
		t.Fatal("sending granted reading", err)
	}
	_, after, _ := l.Binding(ctx)
	if after <= before {
		t.Fatal("permission mutation did not invalidate policy tokens")
	}
	epoch, _, err := l.Binding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seedRetainedSendFacts(t, db, epoch, *now)
	if err := l.SetFullSend(ctx, false); err != nil {
		t.Fatal(err)
	}
	assertRetainedSendFacts(t, db)
	if err := l.SetFullSend(ctx, true); err != nil {
		t.Fatal(err)
	}
	assertRetainedSendFacts(t, db)
	if _, err := r.Acquire(ctx); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent send policy mutation acquired lease", err)
	}
	if _, err := db.Exec("UPDATE authorization_state SET epoch=?", strings.Repeat("b", 43)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM full_send_access").Scan(&count); err != nil || count != 0 {
		t.Fatal("epoch rotation retained send permission", err)
	}
	if _, err := l.FullSend(ctx); !errors.Is(err, ErrEpochChanged) {
		t.Fatal("old lease retained send permission", err)
	}
	assertRetainedSendFacts(t, db)
	if _, err := db.Exec("INSERT INTO full_send_access VALUES(1,?)", strings.Repeat("b", 43)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM authorization_state"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM full_send_access").Scan(&count); err != nil || count != 0 {
		t.Fatal("logout retained send permission", err)
	}
	assertRetainedSendFacts(t, db)
}

func TestFullSendMutationFailureRollsBackPermissionAndRevision(t *testing.T) {
	r, db, _ := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	_, before, _ := l.Binding(ctx)
	if _, err := db.Exec(`CREATE TRIGGER reject_full_send BEFORE INSERT ON full_send_access BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if l.SetFullSend(ctx, true) == nil {
		t.Fatal("failed permission mutation accepted")
	}
	enabled, err := l.FullSend(ctx)
	if err != nil || enabled {
		t.Fatal("failed mutation retained authority", err)
	}
	_, after, _ := l.Binding(ctx)
	if before != after {
		t.Fatal("failed mutation changed policy revision")
	}
}
