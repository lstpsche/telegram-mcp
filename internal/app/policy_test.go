package app

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestPeerDiscoveryRequiresEpochBeforeOpeningAccount(t *testing.T) {
	application, _ := newTestApplication(t)
	application.factory = func(tgaccount.Config, *tgaccount.KeychainSessionStorage, tgaccount.Mode) (accountRuntime, error) {
		t.Fatal("account constructed without authorization epoch")
		return nil, errors.New("unexpected")
	}
	if peers, err := application.Peers(context.Background()); !errors.Is(err, tgaccount.ErrReauthenticationRequired) || peers != nil {
		t.Fatalf("peers=%v err=%v", peers, err)
	}
}

func authorizedTextApplication(t *testing.T, runtime accountRuntime) *Application {
	t.Helper()
	application, _ := newTestApplication(t)
	if err := application.Configure(context.Background(), tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	database, repository, err := application.openRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordAuthorization(context.Background(), "test_authorization_epoch_12345", nil, 2, application.now()); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	application.factory = func(tgaccount.Config, *tgaccount.KeychainSessionStorage, tgaccount.Mode) (accountRuntime, error) {
		return runtime, nil
	}
	return application
}

func TestPeerDiscoveryInitializesAuthorizedReadModeAndDeadline(t *testing.T) {
	runtime := &fakeDiscoveryRuntime{}
	application := authorizedTextApplication(t, runtime)
	application.factory = func(_ tgaccount.Config, _ *tgaccount.KeychainSessionStorage, mode tgaccount.Mode) (accountRuntime, error) {
		if mode != tgaccount.ModeRead {
			t.Fatalf("mode=%v", mode)
		}
		return runtime, nil
	}
	if _, err := application.Peers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !runtime.discovered || runtime.epoch != "test_authorization_epoch_12345" || !runtime.deadline {
		t.Fatalf("discovery state=%+v", runtime)
	}
}

func TestPeerDiscoveryFailureDoesNotReleaseResults(t *testing.T) {
	for _, failure := range []error{context.Canceled, tgaccount.ErrReauthenticationRequired, errors.New("synthetic failure")} {
		runtime := &fakeDiscoveryRuntime{discoverError: failure}
		application := authorizedTextApplication(t, runtime)
		peers, err := application.Peers(context.Background())
		if !errors.Is(err, failure) || peers != nil {
			t.Fatalf("peers=%v err=%v", peers, err)
		}
		lock, err := daemon.AcquireAccountLock(application.paths.Lock)
		if err != nil {
			t.Fatalf("session lock retained: %v", err)
		}
		if err := lock.Release(); err != nil {
			t.Fatal(err)
		}
		if errors.Is(failure, tgaccount.ErrReauthenticationRequired) {
			status, err := application.Status(context.Background())
			if err != nil || status.Authorized {
				t.Fatalf("reauth did not invalidate epoch: %+v %v", status, err)
			}
		}
	}
}

func TestPeerDiscoveryInitializationFailureStopsBeforeDiscovery(t *testing.T) {
	failure := errors.New("initialization failed")
	runtime := &fakeDiscoveryRuntime{enableError: failure}
	application := authorizedTextApplication(t, runtime)
	if _, err := application.Peers(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if runtime.discovered {
		t.Fatal("discovery started despite initialization failure")
	}
}

func TestGrantsRemainMutableWhileAccountLockIsHeld(t *testing.T) {
	application := authorizedTextApplication(t, &fakeRuntime{})
	lock, err := daemon.AcquireAccountLock(application.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	author, _ := model.ParsePeerID("tgpeer:v1:user:456")
	grant := policy.Grant{Peer: peer, Author: author, MinID: 1, MaxID: 10, ReadThrough: 10, Profile: policy.ProfileConsented, ExpiresAt: application.now().Add(time.Hour), Eligible: true}
	if err := application.Grant(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	grants, err := application.Grants(context.Background())
	if err != nil || len(grants) != 1 || grants[0].Peer != peer {
		t.Fatalf("grants=%v err=%v", grants, err)
	}
	if err := application.Revoke(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	grants, err = application.Grants(context.Background())
	if err != nil || len(grants) != 0 {
		t.Fatalf("grants=%v err=%v", grants, err)
	}
}

type fakeDiscoveryRuntime struct {
	fakeRuntime
	epoch         string
	discovered    bool
	deadline      bool
	enableError   error
	discoverError error
}

func (f *fakeDiscoveryRuntime) EnableReads(ctx context.Context, _ *sql.DB, epoch string) error {
	f.epoch = epoch
	_, f.deadline = ctx.Deadline()
	return f.enableError
}
func (f *fakeDiscoveryRuntime) Discover(context.Context) ([]model.Chat, error) {
	f.discovered = true
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	return []model.Chat{{ID: peer, Title: "untrusted display"}}, f.discoverError
}

func (f *fakeDiscoveryRuntime) DiscoverSavedMessage(ctx context.Context) (model.MessageID, error) {
	f.discovered = true
	_, f.deadline = ctx.Deadline()
	id, _ := model.ParseMessageID("tgmsg:v1:self:1:20")
	return id, f.discoverError
}

func TestSavedMessageDiscoveryOwnsSessionAndDoesNotGrant(t *testing.T) {
	ctx := context.Background()
	runtime := &fakeDiscoveryRuntime{}
	application := authorizedTextApplication(t, runtime)
	lock, err := daemon.AcquireAccountLock(application.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.SavedMessage(ctx); !errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatal("discovery bypassed session ownership", err)
	}
	if runtime.discovered {
		t.Fatal("locked discovery reached runtime")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	before := authorizationState(t, application.paths.Database)
	id, err := application.SavedMessage(ctx)
	if err != nil || id.String() != "tgmsg:v1:self:1:20" || !runtime.deadline || runtime.epoch != before.Epoch {
		t.Fatal("discovery failed", err)
	}
	grants, err := application.Grants(ctx)
	if err != nil || len(grants) != 0 {
		t.Fatal("discovery created authority", err)
	}
	if after := authorizationState(t, application.paths.Database); before.Epoch != after.Epoch {
		t.Fatal("discovery changed authorization epoch")
	}
	runtime.discoverError = errors.New("synthetic discovery failure")
	if id, err := application.SavedMessage(ctx); !errors.Is(err, runtime.discoverError) || id.String() != "" {
		t.Fatal("failed discovery released a reference", err)
	}
	runtime.discoverError = tgaccount.ErrReauthenticationRequired
	if _, err := application.SavedMessage(ctx); !errors.Is(err, runtime.discoverError) {
		t.Fatal("authorization cause lost", err)
	}
	status, err := application.Status(ctx)
	if err != nil || status.Authorized {
		t.Fatal("revoked session retained authority", err)
	}
}
