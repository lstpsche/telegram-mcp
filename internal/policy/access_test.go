package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestFullReadAuthorityLifecycle(t *testing.T) {
	r, db, now := fixture(t)
	ctx := context.Background()
	l := leaseFor(t, r)
	g := validGrant(t, *now)
	if enabled, err := l.FullRead(ctx); err != nil || enabled {
		t.Fatalf("default: %t %v", enabled, err)
	}
	if _, err := l.Grant(ctx, g.Peer); !errors.Is(err, ErrDenied) {
		t.Fatal("default granted access", err)
	}
	if err := l.Save(ctx, g); err != nil {
		t.Fatal(err)
	}
	_, before, _ := l.Binding(ctx)
	if err := l.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	_, enabledRevision, _ := l.Binding(ctx)
	if enabledRevision <= before {
		t.Fatal("mode did not invalidate tokens")
	}
	// Existing grants remain exact records; full access creates no per-peer rows.
	rows, err := l.List(ctx)
	if err != nil || len(rows) != 1 || rows[0] != g {
		t.Fatal("restricted grants altered", err)
	}
	other := peer(t, model.PeerKindChannel, 99)
	authority, err := l.Grant(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	messageID, _ := model.NewMessageID(other, 2147483647)
	c := model.Candidate{Message: model.Message{ID: messageID, Author: peer(t, model.PeerKindUser, 777), Text: "synthetic"}}
	later := now.AddDate(1, 0, 0)
	if err := authority.CheckMessage(c, g.Author, later); err != nil {
		t.Fatal("full access rejected future author/range", err)
	}
	if err := authority.CheckRead(2147483647, later); err != nil {
		t.Fatal(err)
	}
	if !authority.Images || !authority.ExpiresAt.IsZero() || authority.Deadline(later) != later {
		t.Fatal("full authority invented expiry or withheld images")
	}
	for _, unsafe := range []model.Candidate{{Message: c.Message, Protected: true}, {Message: c.Message, Ephemeral: true}, {Message: c.Message, Unsupported: true}, {Message: c.Message, Forwarded: true}} {
		if authority.CheckMessage(unsafe, g.Author, later) == nil {
			t.Fatal("full mode bypassed content validation")
		}
	}
	wrong := c
	wrong.Message.ID, _ = model.NewMessageID(g.Peer, 20)
	if authority.CheckMessage(wrong, g.Author, later) == nil {
		t.Fatal("cross-peer accepted")
	}
	if err := l.SetFullRead(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Grant(ctx, other); !errors.Is(err, ErrDenied) {
		t.Fatal("disabled mode retained access", err)
	}
	if restored, err := l.Grant(ctx, g.Peer); err != nil || restored != g {
		t.Fatal("restricted grant not restored", err)
	}
	if err := l.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE authorization_state SET epoch=?", strings.Repeat("b", 43)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM full_read_access").Scan(&count); err != nil || count != 0 {
		t.Fatal("epoch retained full mode", count, err)
	}
	if _, err := l.FullRead(ctx); !errors.Is(err, ErrEpochChanged) {
		t.Fatal("old lease retained authority", err)
	}
}

func TestFullReadMutationRollbackAndDeletion(t *testing.T) {
	r, db, _ := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	_, before, _ := l.Binding(ctx)
	if _, err := db.Exec(`CREATE TRIGGER fail_full_read BEFORE INSERT ON full_read_access BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if l.SetFullRead(ctx, true) == nil {
		t.Fatal("failed mutation accepted")
	}
	enabled, err := l.FullRead(ctx)
	if err != nil || enabled {
		t.Fatal(enabled, err)
	}
	_, after, _ := l.Binding(ctx)
	if after != before {
		t.Fatal("rollback changed revision")
	}
	if _, err := db.Exec("DROP TRIGGER fail_full_read"); err != nil {
		t.Fatal(err)
	}
	if err := l.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM authorization_state"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM full_read_access").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestFullReadEnableRequiresLeaseAndCannotBeStoredAsExactGrant(t *testing.T) {
	r, _, now := fixture(t)
	l := leaseFor(t, r)
	if _, err := r.Acquire(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent mode mutation acquired lease", err)
	}
	if err := l.Save(context.Background(), fullReadGrant(peer(t, model.PeerKindChat, 4))); err == nil {
		t.Fatal("full authority stored as per-peer grant")
	}
	if err := fullReadGrant(model.PeerID{}).CheckCurrent(*now); err == nil {
		t.Fatal("empty full peer accepted")
	}
	if err := fullReadGrant(peer(t, model.PeerKindUser, 1)).CheckCurrent(time.Time{}); err == nil {
		t.Fatal("empty clock accepted")
	}
}
