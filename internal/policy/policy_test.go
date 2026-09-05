package policy

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func fixture(t *testing.T) (*Repository, *sql.DB, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	directory := filepath.Join(t.TempDir(), "state")
	db, err := store.Open(context.Background(), filepath.Join(directory, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("INSERT INTO authorization_state VALUES (1,?,?,?)", strings.Repeat("a", 43), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	repository, err := New(db, filepath.Join(directory, "policy.lock"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return repository, db, &now
}

func peer(t *testing.T, kind model.PeerKind, id int64) model.PeerID {
	t.Helper()
	peer, err := model.NewPeerID(kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return peer
}

func validGrant(t *testing.T, now time.Time) Grant {
	return Grant{Peer: peer(t, model.PeerKindChat, 11), Author: peer(t, model.PeerKindUser, 22), MinID: 10, MaxID: 20, ReadThrough: 15, Profile: ProfileSelfAuthored, ExpiresAt: now.Add(time.Hour), Eligible: true}
}

func leaseFor(t *testing.T, r *Repository) *Lease {
	t.Helper()
	lease, err := r.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lease.Close(); err != nil {
			t.Error(err)
		}
	})
	return lease
}

func TestGrantContentAndReadPrefixAreIndependent(t *testing.T) {
	now := time.Now()
	grant := validGrant(t, now)
	messageID, _ := model.NewMessageID(grant.Peer, 18)
	candidate := model.Candidate{Message: model.Message{ID: messageID, Author: grant.Author, Text: "synthetic"}}
	if err := grant.CheckMessage(candidate, grant.Author, now); err != nil {
		t.Fatal(err)
	}
	if err := grant.CheckRead(18, now); !errors.Is(err, ErrDenied) {
		t.Fatalf("read outside prefix: %v", err)
	}
	if err := grant.CheckRead(5, now); err != nil {
		t.Fatalf("prefix includes undisplayed older messages: %v", err)
	}
	for _, id := range []int32{0, 16, 21} {
		if err := grant.CheckRead(id, now); !errors.Is(err, ErrDenied) {
			t.Fatalf("read %d accepted", id)
		}
	}
	for _, id := range []int32{9, 21} {
		candidate.Message.ID, _ = model.NewMessageID(grant.Peer, id)
		if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
			t.Fatalf("content %d accepted", id)
		}
	}
	candidate.Message.ID = messageID
	candidate.Message.Author = peer(t, model.PeerKindUser, 23)
	if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
		t.Fatal("other author accepted")
	}
	candidate.Message.Author = grant.Author
	if err := grant.CheckMessage(candidate, peer(t, model.PeerKindUser, 23), now); !errors.Is(err, ErrDenied) {
		t.Fatal("self-authored claim accepted for another account")
	}
	grant.Profile = ProfileConsented
	if err := grant.CheckMessage(candidate, peer(t, model.PeerKindUser, 23), now); err != nil {
		t.Fatal(err)
	}
	otherPeer := peer(t, model.PeerKindChat, 12)
	candidate.Message.ID, _ = model.NewMessageID(otherPeer, 18)
	if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
		t.Fatal("other peer accepted")
	}
	candidate.Message.ID = messageID
	for _, unsafe := range []model.Candidate{
		{Message: candidate.Message, Protected: true}, {Message: candidate.Message, Ephemeral: true},
		{Message: candidate.Message, Forwarded: true}, {Message: candidate.Message, Quoted: true}, {Message: candidate.Message, Unsupported: true},
	} {
		if err := grant.CheckMessage(unsafe, grant.Author, now); !errors.Is(err, ErrDenied) {
			t.Fatal("unsafe candidate accepted")
		}
	}
	if err := grant.CheckMessage(candidate, grant.Author, grant.ExpiresAt); !errors.Is(err, ErrDenied) {
		t.Fatal("expired grant accepted")
	}
}

func TestInvalidGrants(t *testing.T) {
	now := time.Now()
	changes := []func(*Grant){
		func(g *Grant) { g.Peer = model.PeerID{} }, func(g *Grant) { g.Author = model.PeerID{} },
		func(g *Grant) { g.Author = peer(t, model.PeerKindChannel, 22) }, func(g *Grant) { g.MinID = 0 },
		func(g *Grant) { g.MaxID = g.MinID - 1 }, func(g *Grant) { g.ReadThrough = -1 }, func(g *Grant) { g.Eligible = false },
		func(g *Grant) { g.Profile = "arbitrary attestation" }, func(g *Grant) { g.ExpiresAt = now },
		func(g *Grant) { g.ExpiresAt = now.Add(MaximumGrantLifetime + time.Second) },
	}
	for i, change := range changes {
		grant := validGrant(t, now)
		change(&grant)
		if err := grant.Validate(now); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestRepositoryDeniesAndSerializesAcrossInstances(t *testing.T) {
	r, db, now := fixture(t)
	ctx := context.Background()
	grant := validGrant(t, *now)
	lease := leaseFor(t, r)
	if _, err := lease.Grant(ctx, grant.Peer); !errors.Is(err, ErrDenied) {
		t.Fatalf("default deny: %v", err)
	}
	if list, err := lease.List(ctx); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty current grants: %#v %v", list, err)
	}
	r2, err := New(db, r.lockPath, r.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Acquire(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent grant/revoke should be busy: %v", err)
	}
	if err := lease.Save(ctx, grant); err != nil {
		t.Fatal(err)
	}
	got, err := lease.Grant(ctx, grant.Peer)
	if err != nil || got != grant {
		t.Fatalf("loaded grant: %#v %v", got, err)
	}
	// A lease leaves the connection available for checkpoint writers.
	if _, err := db.Exec("UPDATE authorization_state SET updated_at = updated_at"); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Save(ctx, grant); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed lease accepted write: %v", err)
	}
	second := leaseFor(t, r2)
	if err := second.Revoke(ctx, grant.Peer); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Grant(ctx, grant.Peer); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked grant accepted: %v", err)
	}
}

func TestExpiryAndAuthorizationCleanup(t *testing.T) {
	for _, change := range []string{"UPDATE authorization_state SET epoch = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'", "DELETE FROM authorization_state"} {
		t.Run(change[:6], func(t *testing.T) {
			r, db, now := fixture(t)
			ctx := context.Background()
			lease := leaseFor(t, r)
			grant := validGrant(t, *now)
			grant.Images = true
			if err := lease.Save(ctx, grant); err != nil {
				t.Fatal(err)
			}
			*now = grant.ExpiresAt
			if _, err := lease.Grant(ctx, grant.Peer); !errors.Is(err, ErrDenied) {
				t.Fatalf("expired accepted: %v", err)
			}
			if list, err := lease.List(ctx); err != nil || len(list) != 0 {
				t.Fatalf("expired listed: %#v %v", list, err)
			}
			if _, err := db.Exec(change); err != nil {
				t.Fatal(err)
			}
			if _, err := lease.List(ctx); !errors.Is(err, ErrEpochChanged) {
				t.Fatalf("stale lease: %v", err)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM text_grants").Scan(&count); err != nil || count != 0 {
				t.Fatalf("grants retained: %d %v", count, err)
			}
		})
	}
}

func TestGrantLimitAndCorruptMetadata(t *testing.T) {
	r, db, now := fixture(t)
	ctx := context.Background()
	lease := leaseFor(t, r)
	grant := validGrant(t, *now)
	for i := 1; i <= MaximumGrants; i++ {
		grant.Peer = peer(t, model.PeerKindChat, int64(i))
		if err := lease.Save(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := lease.Save(ctx, grant); err != nil {
		t.Fatal("replacement at limit failed", err)
	}
	grant.Peer = peer(t, model.PeerKindChat, 100)
	if err := lease.Save(ctx, grant); err == nil {
		t.Fatal("grant limit exceeded")
	}
	if _, err := db.Exec("UPDATE text_grants SET author = 'hostile secret' WHERE peer = ?", peer(t, model.PeerKindChat, 1).String()); err != nil {
		t.Fatal(err)
	}
	*now = grant.ExpiresAt
	_, err := lease.List(ctx)
	if err == nil || strings.Contains(err.Error(), "hostile") {
		t.Fatalf("corrupt expired record not rejected safely: %v", err)
	}
}

func TestAuditUsesClosedContentFreeFields(t *testing.T) {
	r, db, _ := fixture(t)
	ctx := context.Background()
	lease := leaseFor(t, r)
	if err := lease.Audit(ctx, "req_synthetic", "list_messages", "", 2, false); err != nil {
		t.Fatal(err)
	}
	if err := lease.Audit(ctx, "req_denied", "get_message_context", model.ErrorPolicyDenied, 0, false); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id, op    string
		cat       model.ErrorCategory
		count     int
		uncertain bool
	}{
		{"message body", "list_messages", "", 0, false}, {"req_a", "hostile operation", "", 0, false},
		{"req_a", "list_messages", "arbitrary", 0, false}, {"req_a", "list_messages", "", 0, true},
		{"req_a", "list_messages", model.ErrorPolicyDenied, 1, false},
	}
	for _, test := range cases {
		if err := lease.Audit(ctx, test.id, test.op, test.cat, test.count, test.uncertain); err == nil {
			t.Fatal("invalid audit accepted")
		}
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM text_audit").Scan(&count); err != nil || count != 2 {
		t.Fatalf("audit count: %d %v", count, err)
	}
	if _, err := db.Exec("DROP TABLE text_audit"); err != nil {
		t.Fatal(err)
	}
	if err := lease.Audit(ctx, "req_fail", "list_messages", "", 1, false); err == nil {
		t.Fatal("audit storage failure concealed")
	}
}

func TestImagePermissionRequiresOptInAndPreservesContentBounds(t *testing.T) {
	now := time.Now()
	grant := validGrant(t, now)
	id, _ := model.NewMessageID(grant.Peer, 18)
	candidate := model.Candidate{Message: model.Message{ID: id, Author: grant.Author}, Image: &model.ImageSource{}}
	if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
		t.Fatal("text grant permitted image metadata", err)
	}
	grant.Images = true
	if err := grant.CheckMessage(candidate, grant.Author, now); err != nil {
		t.Fatal("image permission rejected", err)
	}
	if err := grant.CheckRead(18, now); !errors.Is(err, ErrDenied) {
		t.Fatal("image permission expanded read prefix", err)
	}
	candidate.Message.Author = peer(t, model.PeerKindUser, 23)
	if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
		t.Fatal("image permission expanded authors", err)
	}
	candidate.Message.Author = grant.Author
	candidate.Protected = true
	if err := grant.CheckMessage(candidate, grant.Author, now); !errors.Is(err, ErrDenied) {
		t.Fatal("image permission allowed protected content", err)
	}
}

func TestImagePermissionPersistsAndReplacementDisablesIt(t *testing.T) {
	r, _, now := fixture(t)
	ctx := context.Background()
	lease := leaseFor(t, r)
	grant := validGrant(t, *now)
	for _, allowed := range []bool{false, true, false} {
		_, before, err := lease.Binding(ctx)
		if err != nil {
			t.Fatal(err)
		}
		grant.Images = allowed
		if err := lease.Save(ctx, grant); err != nil {
			t.Fatal(err)
		}
		got, err := lease.Grant(ctx, grant.Peer)
		if err != nil || got != grant {
			t.Fatalf("image permission round trip: %+v %v", got, err)
		}
		listed, err := lease.List(ctx)
		if err != nil || len(listed) != 1 || listed[0] != grant {
			t.Fatalf("image permission listing: %+v %v", listed, err)
		}
		_, after, err := lease.Binding(ctx)
		if err != nil || after <= before {
			t.Fatal("image permission mutation did not invalidate authority", err)
		}
	}
	if err := lease.Audit(ctx, "req_image", "open_image", "", 1, false); err != nil {
		t.Fatal("image audit failed", err)
	}
	if err := lease.Audit(ctx, "req_uncertain_image", "open_image", model.ErrorReadEffectUncertain, 0, true); err != nil {
		t.Fatal("uncertain image audit failed", err)
	}
}
