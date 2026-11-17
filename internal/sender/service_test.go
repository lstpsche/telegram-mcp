package sender

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

type fakeBackend struct {
	notReady     bool
	readyCalls   int
	resolveCalls int
	prepareCalls int
	sendCalls    int
	query        model.ResolveRecipientQuery
	commands     []model.SendAttempt
	preparations []model.SendAttempt
	nilPrepared  bool
	resolve      func(context.Context, model.ResolveRecipientQuery) (model.ResolvedRecipient, error)
	prepare      func(context.Context, model.SendAttempt) error
	send         func(context.Context, model.SendAttempt) (model.SendAttemptResult, error)
}

func (f *fakeBackend) Ready() bool { f.readyCalls++; return !f.notReady }

func (f *fakeBackend) ResolveRecipient(ctx context.Context, query model.ResolveRecipientQuery) (model.ResolvedRecipient, error) {
	f.resolveCalls++
	f.query = query
	if f.resolve != nil {
		return f.resolve(ctx, query)
	}
	return model.ResolvedRecipient{Peer: query.Peer, Kind: query.Peer.Kind()}, nil
}

func (f *fakeBackend) PrepareSend(ctx context.Context, command model.SendAttempt) (model.PreparedSend, error) {
	f.prepareCalls++
	f.preparations = append(f.preparations, command)
	if f.prepare != nil {
		if err := f.prepare(ctx, command); err != nil {
			return nil, err
		}
	}
	if f.nilPrepared {
		return nil, nil
	}
	return func(ctx context.Context) (model.SendAttemptResult, error) {
		f.sendCalls++
		f.commands = append(f.commands, command)
		if f.send != nil {
			return f.send(ctx, command)
		}
		return model.SendAttemptResult{}, nil
	}, nil
}

type fixture struct {
	service    *Service
	backend    *fakeBackend
	policy     *policy.Repository
	operations *store.OutgoingRepository
	database   *sql.DB
	now        time.Time
	epoch      string
	peer       model.PeerID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC), epoch: strings.Repeat("e", 43), backend: &fakeBackend{}}
	directory := filepath.Join(t.TempDir(), "private")
	var err error
	f.database, err = store.Open(context.Background(), filepath.Join(directory, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.database.Close() })
	if _, err := f.database.Exec("INSERT INTO authorization_state VALUES (1,?,?,?)", f.epoch, f.now.Format(time.RFC3339Nano), f.now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	f.policy, err = policy.New(f.database, filepath.Join(directory, "policy.lock"), func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.operations, err = store.NewOutgoingRepository(f.database)
	if err != nil {
		t.Fatal(err)
	}
	f.peer, err = model.NewPeerID(model.PeerKindUser, 17)
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = New(f.backend, f.policy, f.operations, func() time.Time { return f.now }, rand.Reader, bytes.Repeat([]byte{0x71}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) permission(t *testing.T, send, read bool) {
	t.Helper()
	lease, err := f.policy.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if err := lease.SetFullSend(context.Background(), send); err != nil {
		t.Fatal(err)
	}
	if err := lease.SetFullRead(context.Background(), read); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) request(t *testing.T, text string) model.SendRequest {
	t.Helper()
	id, err := model.NewOperationID(f.now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return model.SendRequest{OperationID: id, Peer: f.peer, Text: text}
}

func (f *fixture) admit(t *testing.T, request model.SendRequest) store.OutgoingOperation {
	t.Helper()
	operation, _, err := f.operations.Admit(context.Background(), store.OutgoingOperation{OperationID: request.OperationID, AuthorizationEpoch: f.epoch, Peer: request.Peer, ReplyTo: request.ReplyTo, PayloadDigest: f.service.payloadDigest(request), RandomID: 1234}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func onlyOperation(t *testing.T, result Result) model.SendOperation {
	t.Helper()
	var envelope model.Envelope[model.SendOperation]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 || envelope.ReadEffect.Kind != model.ReadEffectNone {
		t.Fatalf("invalid send envelope: %s", result.JSON)
	}
	return envelope.Items[0]
}

func TestSendPermissionIsIndependentOfReadAccess(t *testing.T) {
	f := newFixture(t)
	for _, fullRead := range []bool{false, true} {
		f.permission(t, false, fullRead)
		request := f.request(t, "synthetic text")
		for _, invoke := range []func() error{
			func() error {
				_, err := f.service.ResolveRecipient(context.Background(), "req_resolve", model.ResolveRecipientQuery{Peer: f.peer})
				return err
			},
			func() error { _, err := f.service.SendMessage(context.Background(), "req_send", request); return err },
			func() error {
				_, err := f.service.SendStatus(context.Background(), "req_status", request.OperationID)
				return err
			},
		} {
			if err := invoke(); model.TextErrorCategory(err) != model.ErrorPolicyDenied {
				t.Fatalf("read access %v granted send authority: %v", fullRead, err)
			}
		}
	}
	if f.backend.readyCalls != 0 || f.backend.resolveCalls != 0 || f.backend.prepareCalls != 0 || f.backend.sendCalls != 0 {
		t.Fatal("denied operation reached backend")
	}
	f.permission(t, true, false)
	result, err := f.service.ResolveRecipient(context.Background(), "req_allowed", model.ResolveRecipientQuery{Peer: f.peer})
	if err != nil || len(result.JSON) == 0 || f.backend.resolveCalls != 1 {
		t.Fatalf("send access requires no read access: %v", err)
	}
}

func TestResolveRecipientIssuesFreshKeyWithoutLedgerAdmission(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	seen := make(map[string]bool)
	for range 2 {
		result, err := f.service.ResolveRecipient(context.Background(), "req_resolve", model.ResolveRecipientQuery{Peer: f.peer})
		if err != nil {
			t.Fatal(err)
		}
		var envelope model.Envelope[model.ResolvedRecipient]
		if err := json.Unmarshal(result.JSON, &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Items) != 1 || envelope.Items[0].Peer != f.peer {
			t.Fatal("wrong recipient")
		}
		created, err := model.ParseOperationID(envelope.Items[0].OperationID)
		if err != nil || !created.Equal(f.now) {
			t.Fatalf("key does not use injected clock: %v", err)
		}
		if seen[envelope.Items[0].OperationID] {
			t.Fatal("recipient resolution repeated an operation key")
		}
		seen[envelope.Items[0].OperationID] = true
		if _, found, err := f.operations.Find(context.Background(), envelope.Items[0].OperationID, f.now); err != nil || found {
			t.Fatalf("key issuance reserved an operation: %v", err)
		}
	}
}

func TestLocalStatusAndReceiptReplayDoNotRequireBackendReadiness(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "synthetic message")
	operation := f.admit(t, request)
	if _, _, err := f.operations.RecordReceipt(context.Background(), f.epoch, operation.RandomID, 82, nil, f.now); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(48 * time.Hour)
	f.backend.notReady = true
	for _, invoke := range []func() (Result, error){
		func() (Result, error) {
			return f.service.SendStatus(context.Background(), "req_status", request.OperationID)
		},
		func() (Result, error) { return f.service.SendMessage(context.Background(), "req_replay", request) },
	} {
		result, err := invoke()
		if err != nil {
			t.Fatal(err)
		}
		operation := onlyOperation(t, result)
		if operation.State != model.SendSent || operation.MessageID == nil || operation.MessageID.TelegramID() != 82 {
			t.Fatal("known receipt was lost")
		}
	}
	if f.backend.readyCalls != 0 || f.backend.sendCalls != 0 || f.backend.prepareCalls != 0 {
		t.Fatal("retained facts consulted backend readiness")
	}
}

func TestUnknownAndWrongEpochStatusAreUnavailable(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "synthetic message")
	if _, err := f.service.SendStatus(context.Background(), "req_missing", request.OperationID); model.TextErrorCategory(err) != model.ErrorOperationUnavailable {
		t.Fatalf("missing operation: %v", err)
	}
	f.admit(t, request)
	if _, err := f.database.Exec("UPDATE outgoing_operations SET authorization_epoch=?", strings.Repeat("x", 43)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SendStatus(context.Background(), "req_other", request.OperationID); model.TextErrorCategory(err) != model.ErrorOperationUnavailable {
		t.Fatalf("other epoch exposed: %v", err)
	}
	if f.backend.readyCalls != 0 {
		t.Fatal("status checked live readiness")
	}
}

func TestPayloadDigestBindsExactBytesAndReply(t *testing.T) {
	f := newFixture(t)
	request := f.request(t, " synthetic text ")
	initial := f.service.payloadDigest(request)
	changed := request
	changed.Text = strings.TrimSpace(request.Text)
	if initial == f.service.payloadDigest(changed) {
		t.Fatal("text whitespace was normalized")
	}
	reply, _ := model.NewMessageID(f.peer, 31)
	changed = request
	changed.ReplyTo = &reply
	if initial == f.service.payloadDigest(changed) {
		t.Fatal("reply identity was omitted")
	}
	other := &Service{integrityKey: f.service.integrityKey}
	other.integrityKey[0] ^= 1
	if initial == other.payloadDigest(request) {
		t.Fatal("payload digest was not keyed")
	}
}

func TestNilServiceReturnsNotReady(t *testing.T) {
	var service *Service
	if service.Ready() {
		t.Fatal("nil service ready")
	}
	for _, invoke := range []func() error{
		func() error {
			_, err := service.ResolveRecipient(context.Background(), "req_nil", model.ResolveRecipientQuery{})
			return err
		},
		func() error {
			_, err := service.SendMessage(context.Background(), "req_nil", model.SendRequest{})
			return err
		},
		func() error { _, err := service.SendStatus(context.Background(), "req_nil", ""); return err },
	} {
		if err := invoke(); model.TextErrorCategory(err) != model.ErrorNotReady {
			t.Fatalf("nil service: %v", err)
		}
	}
}
