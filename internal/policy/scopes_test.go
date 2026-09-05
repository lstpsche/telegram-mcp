package policy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestScopeLifecyclePreservesIdentityWithoutGrantingAccess(t *testing.T) {
	r, db, _ := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	peers := []model.PeerID{peer(t, model.PeerKindUser, 2), peer(t, model.PeerKindChat, 10), peer(t, model.PeerKindSelf, 1)}
	initial, err := l.Scopes(ctx)
	if err != nil || initial == nil || len(initial) != 0 {
		t.Fatal("empty inventory failed", err)
	}
	scope, err := l.SaveScope(ctx, "", "work", peers)
	if err != nil || scope.ID.String() == "" || scope.Name != "work" || len(scope.Peers) != 3 || scope.Peers[0] != peers[1] {
		t.Fatal("scope creation failed", err)
	}
	if _, err := l.Grant(ctx, peers[0]); !errors.Is(err, ErrDenied) {
		t.Fatal("scope membership granted access")
	}
	loaded, err := l.Scope(ctx, scope.ID)
	if err != nil || !reflect.DeepEqual(scope, loaded) {
		t.Fatal("scope round trip failed", err)
	}
	replacement, err := l.SaveScope(ctx, "", "work", peers[:1])
	if err != nil || replacement.ID != scope.ID || len(replacement.Peers) != 1 {
		t.Fatal("replacement changed identity", err)
	}
	renamed, err := l.SaveScope(ctx, scope.ID, "team", nil)
	if err != nil || renamed.ID != scope.ID || renamed.Peers == nil || len(renamed.Peers) != 0 {
		t.Fatal("rename or empty scope failed", err)
	}
	_, beforeDelete, err := l.Binding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.DeleteScope(ctx, scope.ID); err != nil {
		t.Fatal(err)
	}
	_, afterDelete, err := l.Binding(ctx)
	if err != nil || afterDelete <= beforeDelete {
		t.Fatal("deletion did not invalidate authority binding", err)
	}
	if _, err := l.Scope(ctx, scope.ID); model.TextErrorCategory(err) != model.ErrorInvalidReference {
		t.Fatal("deleted scope resolved")
	}
	recreated, err := l.SaveScope(ctx, "", "team", nil)
	if err != nil || recreated.ID == scope.ID {
		t.Fatal("recreated scope reused identity")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM named_scope_peers").Scan(&count); err != nil || count != 0 {
		t.Fatal("membership retained after replace/delete", err)
	}
}

func TestScopeValidationAndLimitsAreAtomic(t *testing.T) {
	r, _, _ := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	member := peer(t, model.PeerKindChat, 1)
	first, err := l.SaveScope(ctx, "", "first", []model.PeerID{member})
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.SaveScope(ctx, "", "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	unknown := model.ScopeID("tgscope:v1:" + strings.Repeat("0", 32))
	tooMany := make([]model.PeerID, MaximumScopePeers+1)
	for i := range tooMany {
		tooMany[i] = peer(t, model.PeerKindChat, int64(i+1))
	}
	_, before, _ := l.Binding(ctx)
	for _, test := range []struct {
		id       model.ScopeID
		name     string
		peers    []model.PeerID
		category model.ErrorCategory
	}{
		{first.ID, "unsafe\nname", nil, model.ErrorInvalidInput},
		{first.ID, "first", []model.PeerID{member, member}, model.ErrorInvalidInput},
		{first.ID, "first", []model.PeerID{{}}, model.ErrorInvalidInput},
		{first.ID, "first", []model.PeerID{peer(t, model.PeerKindChannel, 1)}, model.ErrorInvalidInput},
		{first.ID, "first", tooMany, model.ErrorInvalidInput},
		{first.ID, "second", nil, model.ErrorInvalidInput},
		{unknown, "missing", nil, model.ErrorInvalidReference},
		{"invalid", "missing", nil, model.ErrorInvalidReference},
	} {
		if _, err := l.SaveScope(ctx, test.id, test.name, test.peers); model.TextErrorCategory(err) != test.category {
			t.Fatalf("mutation category = %s, want %s", model.TextErrorCategory(err), test.category)
		}
	}
	if err := l.DeleteScope(ctx, unknown); model.TextErrorCategory(err) != model.ErrorInvalidReference {
		t.Fatal("unknown scope deletion accepted")
	}
	_, after, _ := l.Binding(ctx)
	loaded, err := l.Scope(ctx, first.ID)
	if err != nil || !reflect.DeepEqual(loaded, first) || after != before {
		t.Fatal("rejected mutations changed stored scope or revision", err)
	}
	for i := 2; i < MaximumScopes; i++ {
		if _, err := l.SaveScope(ctx, "", fmt.Sprintf("scope_%02d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.SaveScope(ctx, "", "overflow", nil); !errors.Is(err, ErrInvalidScope) {
		t.Fatal("scope count exceeded")
	}
	if _, err := l.SaveScope(ctx, second.ID, "updated", tooMany[:MaximumScopePeers]); err != nil {
		t.Fatal("replacement at limits failed", err)
	}
	scopes, err := l.Scopes(ctx)
	if err != nil || len(scopes) != MaximumScopes {
		t.Fatal("scope inventory at limit failed", err)
	}
	for i := 1; i < len(scopes); i++ {
		if scopes[i-1].ID.String() >= scopes[i].ID.String() {
			t.Fatal("inventory not ID ordered")
		}
	}
}

func TestScopeMutationRevisionRollbackAndEpochPurge(t *testing.T) {
	for _, change := range []string{"UPDATE authorization_state SET epoch = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'", "DELETE FROM authorization_state"} {
		t.Run(change[:6], func(t *testing.T) {
			r, db, now := fixture(t)
			l := leaseFor(t, r)
			ctx := context.Background()
			_, initial, _ := l.Binding(ctx)
			member := peer(t, model.PeerKindChat, 1)
			scope, err := l.SaveScope(ctx, "", "work", []model.PeerID{member})
			if err != nil {
				t.Fatal(err)
			}
			_, before, _ := l.Binding(ctx)
			if before <= initial {
				t.Fatal("creation did not advance revision")
			}
			if _, err := db.Exec(`CREATE TRIGGER reject_scope_peer BEFORE INSERT ON named_scope_peers BEGIN SELECT RAISE(ABORT,'synthetic storage failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := l.SaveScope(ctx, scope.ID, "changed", []model.PeerID{member}); model.TextErrorCategory(err) != model.ErrorInternal {
				t.Fatal("storage failure concealed")
			}
			if _, err := l.SaveScope(ctx, "", "new", []model.PeerID{member}); model.TextErrorCategory(err) != model.ErrorInternal {
				t.Fatal("failed scope creation concealed")
			}
			if scopes, err := l.Scopes(ctx); err != nil || len(scopes) != 1 {
				t.Fatal("failed creation retained scope metadata", err)
			}
			loaded, err := l.Scope(ctx, scope.ID)
			_, after, _ := l.Binding(ctx)
			if err != nil || !reflect.DeepEqual(scope, loaded) || after != before {
				t.Fatal("failed membership replacement not rolled back", err)
			}
			if _, err := db.Exec("DROP TRIGGER reject_scope_peer"); err != nil {
				t.Fatal(err)
			}
			if _, err := l.SaveScope(ctx, scope.ID, "renamed", []model.PeerID{member}); err != nil {
				t.Fatal(err)
			}
			_, after, _ = l.Binding(ctx)
			if after <= before {
				t.Fatal("replacement did not advance revision")
			}
			grant := validGrant(t, *now)
			grant.Peer = member
			if err := l.Save(ctx, grant); err != nil {
				t.Fatal(err)
			}
			if err := l.Revoke(ctx, member); err != nil {
				t.Fatal(err)
			}
			if loaded, err := l.Scope(ctx, scope.ID); err != nil || len(loaded.Peers) != 1 {
				t.Fatal("grant revoke erased scope membership", err)
			}
			if _, err := db.Exec(change); err != nil {
				t.Fatal(err)
			}
			if _, err := l.Scopes(ctx); !errors.Is(err, ErrEpochChanged) {
				t.Fatal("stale lease returned scopes")
			}
			for _, table := range []string{"named_scopes", "named_scope_peers"} {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("scope state survived epoch change", err)
				}
			}
		})
	}
}

func TestScopeCorruptionAndAudit(t *testing.T) {
	r, db, _ := fixture(t)
	l := leaseFor(t, r)
	ctx := context.Background()
	scope, err := l.SaveScope(ctx, "", "work", []model.PeerID{peer(t, model.PeerKindChat, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Audit(ctx, "req_scopes", "list_scopes", "", 1, false); err != nil {
		t.Fatal("scope audit rejected", err)
	}
	if _, err := db.Exec("UPDATE named_scope_peers SET peer = 'hostile secret' WHERE scope_id = ?", scope.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Scopes(ctx); model.TextErrorCategory(err) != model.ErrorInternal || strings.Contains(err.Error(), "hostile") {
		t.Fatal("corrupt membership not rejected safely", err)
	}
}
