package policy

import (
	"context"
	"testing"
)

func TestPolicyRevisionTracksMutationsAndRollback(t *testing.T) {
	p, db, now := fixture(t)
	l := leaseFor(t, p)
	ctx := context.Background()
	g := validGrant(t, *now)
	epoch, initial, err := l.Binding(ctx)
	if err != nil || epoch == "" || initial != 1 {
		t.Fatal("missing initial binding", err)
	}
	for _, operation := range []func() error{func() error { return l.Save(ctx, g) }, func() error { return l.Save(ctx, g) }, func() error { return l.Revoke(ctx, g.Peer) }} {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
		_, revision, err := l.Binding(ctx)
		if err != nil || revision != initial+1 {
			t.Fatal("mutation did not invalidate cursors")
		}
		initial = revision
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE policy_revision SET revision=revision+1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, revision, err := l.Binding(ctx)
	if err != nil || revision != initial {
		t.Fatal("rollback advanced revision")
	}
	for _, operation := range []string{"search_messages", "list_unread"} {
		if err := l.Audit(ctx, "req_metadata", operation, "", 0, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Audit(ctx, "req_invalid", "arbitrary", "", 0, false); err == nil {
		t.Fatal("unknown audit operation allowed")
	}
}
