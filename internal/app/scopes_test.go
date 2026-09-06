package app

import (
	"context"
	"errors"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestScopesPersistWithoutAccountIOOrGrantAuthority(t *testing.T) {
	ctx := context.Background()
	application := authorizedTextApplication(t, &fakeRuntime{})
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		t.Fatal("scope management opened Telegram account")
		return nil, errors.New("unexpected")
	}
	lock, err := daemon.AcquireAccountLock(application.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	saved, err := application.Scope(ctx, "", "work", []model.PeerID{peer})
	if err != nil || saved.ID == "" {
		t.Fatalf("save=%+v err=%v", saved, err)
	}
	scopes, err := application.Scopes(ctx)
	if err != nil || len(scopes) != 1 || scopes[0].ID != saved.ID || len(scopes[0].Peers) != 1 || scopes[0].Peers[0] != peer {
		t.Fatalf("list=%+v err=%v", scopes, err)
	}
	renamed, err := application.Scope(ctx, saved.ID, "renamed", []model.PeerID{})
	if err != nil || renamed.ID != saved.ID || renamed.Name != "renamed" || len(renamed.Peers) != 0 {
		t.Fatalf("rename=%+v err=%v", renamed, err)
	}
	grants, err := application.Grants(ctx)
	if err != nil || len(grants) != 0 {
		t.Fatalf("scope granted authority: grants=%v err=%v", grants, err)
	}
	if err := application.Unscope(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	scopes, err = application.Scopes(ctx)
	if err != nil || len(scopes) != 0 {
		t.Fatalf("remaining scopes=%+v err=%v", scopes, err)
	}
}

func TestScopeControlRequiresAuthorizationEpoch(t *testing.T) {
	ctx := context.Background()
	application, _ := newTestApplication(t)
	if scopes, err := application.Scopes(ctx); !errors.Is(err, policy.ErrEpochChanged) || scopes != nil {
		t.Fatalf("scopes=%+v err=%v", scopes, err)
	}
	if scope, err := application.Scope(ctx, "", "work", nil); !errors.Is(err, policy.ErrEpochChanged) || scope.ID != "" {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
	id, _ := model.ParseScopeID("tgscope:v1:0123456789abcdef0123456789abcdef")
	if err := application.Unscope(ctx, id); !errors.Is(err, policy.ErrEpochChanged) {
		t.Fatal(err)
	}
}
