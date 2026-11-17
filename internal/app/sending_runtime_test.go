package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sendingRuntime struct {
	fakeRuntime
	epoch       string
	operations  *store.OutgoingRepository
	now         func() time.Time
	resolutions atomic.Int32
	sends       atomic.Int32
	notReady    atomic.Bool
}

func (f *sendingRuntime) EnableReads(_ context.Context, _ *sql.DB, epoch string) error {
	f.epoch = epoch
	return nil
}
func (f *sendingRuntime) EnableSending(operations *store.OutgoingRepository) error {
	f.operations = operations
	return nil
}
func (f *sendingRuntime) Ready() bool { return !f.notReady.Load() }
func (f *sendingRuntime) ResolveRecipient(_ context.Context, query model.ResolveRecipientQuery) (model.ResolvedRecipient, error) {
	f.resolutions.Add(1)
	return model.ResolvedRecipient{Peer: query.Peer, Kind: query.Peer.Kind()}, nil
}
func (f *sendingRuntime) PrepareSend(_ context.Context, attempt model.SendAttempt) (model.PreparedSend, error) {
	return func(ctx context.Context) (model.SendAttemptResult, error) { return f.sendMessage(ctx, attempt) }, nil
}
func (f *sendingRuntime) sendMessage(ctx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
	f.sends.Add(1)
	if f.operations == nil || f.epoch == "" {
		return model.SendAttemptResult{}, errors.New("sending was not initialized before observation")
	}
	at := f.now().UTC().Truncate(time.Second)
	_, matched, err := f.operations.RecordReceipt(ctx, f.epoch, attempt.RandomID, 71, &at, f.now())
	if err != nil || !matched {
		return model.SendAttemptResult{}, errors.New("fixture receipt was not recorded")
	}
	id, _ := model.NewMessageID(attempt.Peer, 71)
	return model.SendAttemptResult{Attempted: true, Receipt: &model.SendReceipt{Peer: attempt.Peer, RandomID: attempt.RandomID, MessageID: id, SentAt: at.Format(time.RFC3339)}}, nil
}

func startSendingDaemon(t *testing.T, application *Application) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.RunDaemon(ctx) }()
	waitForLifecycle(t, application, daemon.StateReady)
	connection, err := daemon.DialSocket(ctx, application.paths.Socket)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "sending-runtime-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: connection, Writer: connection}, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = session.Close()
		_ = connection.Close()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("sending daemon did not stop")
		}
	}
	t.Cleanup(stop)
	return session, stop
}

func callSendingRuntime(t *testing.T, session *mcp.ClientSession, name string, args any, wantError bool) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError != wantError {
		t.Fatalf("%s error=%v result=%v", name, err, result)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDaemonSendingUsesExistingRuntimeAndRetainsPermissionAcrossRestart(t *testing.T) {
	runtime := &sendingRuntime{fakeRuntime: fakeRuntime{observeStatus: tgaccount.AuthorizationStatus{Authorized: true}}}
	application := authorizedTextApplication(t, runtime)
	runtime.now = application.now
	session, stop := startSendingDaemon(t, application)
	if runtime.operations == nil || runtime.epoch == "" {
		t.Fatal("sending sink was not installed")
	}
	args := map[string]any{"peer": "tgpeer:v1:chat:42"}
	denied := callSendingRuntime(t, session, "resolve_recipient", args, true)
	if denied["error"].(map[string]any)["category"] != "policy_denied" || runtime.resolutions.Load() != 0 {
		t.Fatal("send permission did not default deny")
	}
	if err := application.SetFullSend(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	resolved := callSendingRuntime(t, session, "resolve_recipient", args, false)
	item := resolved["items"].([]any)[0].(map[string]any)
	opID := item["operation_id"].(string)
	sendArgs := map[string]any{"peer": item["peer"], "operation_id": opID, "text": "Synthetic runtime message"}
	sent := callSendingRuntime(t, session, "send_message", sendArgs, false)
	if sent["items"].([]any)[0].(map[string]any)["state"] != "sent" || runtime.sends.Load() != 1 {
		t.Fatal("send receipt was not exposed")
	}
	stop()
	if enabled, err := application.FullSend(context.Background()); err != nil || !enabled {
		t.Fatalf("restart lost send permission: %v", err)
	}
	runtime.notReady.Store(true)
	session, _ = startSendingDaemon(t, application)
	status := callSendingRuntime(t, session, "get_send_status", map[string]any{"operation_id": opID}, false)
	if status["items"].([]any)[0].(map[string]any)["state"] != "sent" {
		t.Fatal("offline receipt lookup failed")
	}
	callSendingRuntime(t, session, "send_message", sendArgs, false)
	if runtime.sends.Load() != 1 {
		t.Fatal("restart replay dispatched another send")
	}
	if err := application.SetFullSend(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	callSendingRuntime(t, session, "send_message", sendArgs, true)
	if runtime.sends.Load() != 1 {
		t.Fatal("disabled permission allowed a send")
	}
}

func TestDaemonRecoversInterruptedDispatchBeforeReadiness(t *testing.T) {
	runtime := &sendingRuntime{fakeRuntime: fakeRuntime{observeStatus: tgaccount.AuthorizationStatus{Authorized: true}}}
	application := authorizedTextApplication(t, runtime)
	runtime.now = application.now
	if err := application.SetFullSend(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, repository, err := application.openRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	auth, _, err := repository.Authorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := store.NewOutgoingRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	now := application.now().UTC()
	opID, err := model.NewOperationID(now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	created, _ := model.ParseOperationID(opID)
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:42")
	_, _, err = operations.Admit(ctx, store.OutgoingOperation{OperationID: opID, AuthorizationEpoch: auth.Epoch, Peer: peer, PayloadDigest: [32]byte{1}, RandomID: 9, State: model.SendNotSent, CreatedAt: created, UpdatedAt: now, RetainUntil: created.Add(store.OutgoingRetention)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, began, err := operations.BeginDispatch(ctx, opID, auth.Epoch, now); err != nil || !began {
		t.Fatal("fixture dispatch failed", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	session, _ := startSendingDaemon(t, application)
	status := callSendingRuntime(t, session, "get_send_status", map[string]any{"operation_id": opID}, false)
	if status["items"].([]any)[0].(map[string]any)["state"] != "uncertain" || runtime.sends.Load() != 0 {
		t.Fatal("restart did not conservatively recover dispatch")
	}
}
