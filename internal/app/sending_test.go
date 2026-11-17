package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

type forbiddenSendingSecrets struct{ t *testing.T }

func (s forbiddenSendingSecrets) Get(context.Context, string) ([]byte, error) {
	s.t.Fatal("opened credentials or integrity keys")
	return nil, errors.New("unexpected")
}

func (s forbiddenSendingSecrets) Put(context.Context, string, []byte) error {
	s.t.Fatal("wrote credentials or integrity keys")
	return errors.New("unexpected")
}

func (s forbiddenSendingSecrets) Delete(context.Context, string) error {
	s.t.Fatal("deleted credentials or integrity keys")
	return errors.New("unexpected")
}

func forbidSendingRuntime(t *testing.T, application *Application) {
	t.Helper()
	application.secrets = forbiddenSendingSecrets{t}
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		t.Fatal("opened Telegram")
		return nil, errors.New("unexpected")
	}
}

func TestFullSendControlIsLocalIndependentAndSerialized(t *testing.T) {
	application := authorizedTextApplication(t, &fakeDiscoveryRuntime{})
	forbidSendingRuntime(t, application)
	ctx := context.Background()
	accountLock, err := daemon.AcquireAccountLock(application.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer accountLock.Release()
	assertModes := func(wantRead, wantSend bool) {
		t.Helper()
		read, readErr := application.FullRead(ctx)
		send, sendErr := application.FullSend(ctx)
		if readErr != nil || sendErr != nil || read != wantRead || send != wantSend {
			t.Fatalf("read=%v send=%v errors=%v %v", read, send, readErr, sendErr)
		}
	}
	assertModes(false, false)
	if err := application.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	assertModes(true, false)
	if err := application.SetFullSend(ctx, true); err != nil {
		t.Fatal(err)
	}
	assertModes(true, true)
	restarted, err := New(application.paths, forbiddenSendingSecrets{t})
	if err != nil {
		t.Fatal(err)
	}
	forbidSendingRuntime(t, restarted)
	if enabled, err := restarted.FullSend(ctx); err != nil || !enabled {
		t.Fatal("ordinary restart changed send access", enabled, err)
	}
	if err := application.SetFullRead(ctx, false); err != nil {
		t.Fatal(err)
	}
	assertModes(false, true)
	if err := application.SetFullSend(ctx, false); err != nil {
		t.Fatal(err)
	}
	assertModes(false, false)
	if err := application.withPolicy(ctx, func(_ *policy.Lease) error {
		if err := application.SetFullSend(ctx, true); !errors.Is(err, policy.ErrBusy) {
			t.Fatal("sending mutation raced a policy lease", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFullSendControlRequiresAuthorizationEpoch(t *testing.T) {
	application, _ := newTestApplication(t)
	forbidSendingRuntime(t, application)
	if err := application.SetFullSend(context.Background(), true); !errors.Is(err, policy.ErrEpochChanged) {
		t.Fatal("unconfigured account enabled sending", err)
	}
}

func TestSendStatusDoesNotInitializeAbsentMetadata(t *testing.T) {
	application, _ := newTestApplication(t)
	forbidSendingRuntime(t, application)
	if _, err := application.SendOperationStatus(context.Background(), "01939ab2-f400-7000-8000-000000000001"); !errors.Is(err, ErrSendStatusUnavailable) {
		t.Fatal("missing metadata did not report unavailable", err)
	}
	if _, err := os.Stat(application.paths.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status initialized metadata", err)
	}
}

func TestSendStatusReadsRetainedFactsAfterDisableAndLogoutWithoutRuntimeLocks(t *testing.T) {
	ctx := context.Background()
	application := authorizedTextApplication(t, &fakeRuntime{})
	created := application.now().Truncate(time.Millisecond)
	now := created
	application.now = func() time.Time { return now }
	operationID, err := model.NewOperationID(created, bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if err := application.SetFullSend(ctx, true); err != nil {
		t.Fatal(err)
	}
	database, account, err := application.openRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorization, exists, err := account.Authorization(ctx)
	if err != nil || !exists {
		t.Fatal("missing fixture authorization", err)
	}
	outgoing, err := store.NewOutgoingRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := model.ParsePeerID("tgpeer:v1:user:123")
	candidate := store.OutgoingOperation{OperationID: operationID, AuthorizationEpoch: authorization.Epoch, Peer: peer, RandomID: 98765, PayloadDigest: [32]byte{42}}
	if _, admitted, err := outgoing.Admit(ctx, candidate, now); err != nil || !admitted {
		t.Fatal("admit operation", admitted, err)
	}
	if _, _, err := outgoing.BeginDispatch(ctx, operationID, authorization.Epoch, now); err != nil {
		t.Fatal(err)
	}
	if _, err := outgoing.RecordUncertain(ctx, operationID, authorization.Epoch, model.SendFailureEffectUncertain, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := application.SetFullSend(ctx, false); err != nil {
		t.Fatal(err)
	}
	if status, err := application.SendOperationStatus(ctx, operationID); err != nil || status.State != model.SendUncertain {
		t.Fatal("disabled status", status, err)
	}
	if err := application.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	forbidSendingRuntime(t, application)
	accountLock, err := daemon.AcquireAccountLock(application.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer accountLock.Release()
	policyLock, err := daemon.AcquireAccountLock(filepath.Join(application.paths.StateDir, "policy.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer policyLock.Release()
	now = created.Add(48 * time.Hour)
	status, err := application.SendOperationStatus(ctx, operationID)
	if err != nil || status.OperationID != operationID || status.Peer != peer || status.State != model.SendUncertain || status.FailureCode != model.SendFailureEffectUncertain {
		t.Fatal("retained status after logout", status, err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"payload_digest", "random_id", "authorization_epoch", authorization.Epoch} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("status exposed private metadata", private)
		}
	}
	now = created.Add(store.OutgoingRetention)
	if _, err := application.SendOperationStatus(ctx, operationID); !errors.Is(err, ErrSendStatusUnavailable) {
		t.Fatal("expired operation did not become unavailable", err)
	}
	var retainedRows int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM outgoing_operations WHERE operation_id=?", operationID).Scan(&retainedRows); err != nil || retainedRows != 1 {
		t.Fatal("status pruned or changed metadata", retainedRows, err)
	}
	if _, err := application.SendOperationStatus(ctx, "01939ab2-f400-7000-8000-000000000001"); !errors.Is(err, ErrSendStatusUnavailable) {
		t.Fatal("missing operation did not become unavailable", err)
	}
}
