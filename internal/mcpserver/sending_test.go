package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/sender"
	"github.com/lstpsche/telegram-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func sendingClient(t *testing.T, path string, ctx context.Context) *mcp.ClientSession {
	t.Helper()
	session, _ := sendingClientWithDisconnect(t, path, ctx)
	return session
}

func sendingClientWithDisconnect(t *testing.T, path string, ctx context.Context) (*mcp.ClientSession, func()) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- daemon.Relay(ctx, path, inR, outW) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "sending-wire-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	disconnect := func() {
		// SDK Close drains active calls; close the transport to simulate a lost connection.
		_ = inW.Close()
		_ = outR.Close()
		_ = session.Close()
	}
	t.Cleanup(func() {
		disconnect()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("sending relay did not stop")
		}
	})
	return session, disconnect
}

func assertSendingMirror(t *testing.T, result *mcp.CallToolResult, schema any) map[string]any {
	t.Helper()
	if result == nil || len(result.Content) != 1 {
		t.Fatal("missing sending result")
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("sending result is not JSON text")
	}
	var textValue, structuredValue map[string]any
	if err := json.Unmarshal([]byte(content.Text), &textValue); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &structuredValue); err != nil {
		t.Fatal(err)
	}
	textJSON, _ := json.Marshal(textValue)
	structuredJSON, _ := json.Marshal(structuredValue)
	if string(textJSON) != string(structuredJSON) {
		t.Fatal("sending structured and text content differ")
	}
	encodedSchema, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var output jsonschema.Schema
	if err := json.Unmarshal(encodedSchema, &output); err != nil {
		t.Fatal(err)
	}
	resolved, err := output.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(textValue); err != nil {
		t.Fatalf("sending output does not satisfy advertised schema: %v", err)
	}
	return textValue
}

func TestSendingToolsRemainDiscoverableWithoutRuntime(t *testing.T) {
	path, ctx := serveTestServer(t)
	session := sendingClient(t, path, ctx)
	inventory, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]*mcp.Tool)
	for _, tool := range inventory.Tools {
		names[tool.Name] = tool
	}
	if len(names) != 16 {
		t.Fatalf("unexpected tool inventory: %d", len(names))
	}
	for _, name := range []string{"resolve_recipient", "send_message", "get_send_status"} {
		tool := names[name]
		if tool == nil || tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
			t.Fatalf("missing static sending tool: %s", name)
		}
		if tool.Annotations.ReadOnlyHint != (name != "send_message") {
			t.Fatalf("incorrect side-effect annotation: %s", name)
		}
		var args any
		switch name {
		case "resolve_recipient":
			args = map[string]any{"username": "SyntheticRecipient"}
		case "send_message":
			args = map[string]any{"peer": "tgpeer:v1:chat:42", "operation_id": "019b0000-0000-7000-8000-000000000001", "text": "Synthetic text"}
		case "get_send_status":
			args = map[string]any{"operation_id": "019b0000-0000-7000-8000-000000000001"}
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || !result.IsError {
			t.Fatalf("unavailable service did not fail safely: %s %v", name, err)
		}
		value := assertSendingMirror(t, result, tool.OutputSchema)
		if value["error"].(map[string]any)["category"] != "not_ready" {
			t.Fatalf("unexpected unavailable service error: %v", value)
		}
	}
}

func TestSendingInputErrorsAreClosedAndSanitized(t *testing.T) {
	path, ctx := serveTestServer(t)
	session := sendingClient(t, path, ctx)
	for _, args := range []json.RawMessage{
		json.RawMessage(`null`),
		json.RawMessage(`{"username":null}`),
		json.RawMessage(`{"username":"SyntheticRecipient","phone":"+1234"}`),
		json.RawMessage(`{"username":"SyntheticRecipient","username":"other"}`),
		json.RawMessage(`{"username":"<instruction>hostile-marker</instruction>"}`),
		json.RawMessage(`{"username":"SyntheticRecipient","unknown":"hostile-marker"}`),
		json.RawMessage(`{"username":"` + strings.Repeat("a", 4096) + `"}`),
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "resolve_recipient", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid recipient accepted: %v", err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, `"category":"invalid_input"`) || strings.Contains(text, "hostile-marker") || strings.Contains(text, "SyntheticRecipient") {
			t.Fatalf("unsafe input error: %s", text)
		}
	}
}

type sendingWireBackend struct {
	operations   *store.OutgoingRepository
	peer         model.PeerID
	epoch        string
	sends        atomic.Int32
	postAck      bool
	notReady     atomic.Bool
	resolveError error
	afterReceipt func(context.Context) error
}

func (f *sendingWireBackend) Ready() bool { return !f.notReady.Load() }
func (f *sendingWireBackend) ResolveRecipient(_ context.Context, q model.ResolveRecipientQuery) (model.ResolvedRecipient, error) {
	if f.resolveError != nil {
		return model.ResolvedRecipient{}, f.resolveError
	}
	return model.ResolvedRecipient{Peer: f.peer, Kind: f.peer.Kind(), DisplayName: "Synthetic <instruction>untrusted</instruction>"}, nil
}
func (f *sendingWireBackend) PrepareSend(_ context.Context, attempt model.SendAttempt) (model.PreparedSend, error) {
	return func(ctx context.Context) (model.SendAttemptResult, error) { return f.sendMessage(ctx, attempt) }, nil
}
func (f *sendingWireBackend) sendMessage(ctx context.Context, a model.SendAttempt) (model.SendAttemptResult, error) {
	f.sends.Add(1)
	id, _ := model.NewMessageID(a.Peer, 71)
	at := time.Now().UTC().Truncate(time.Second)
	_, matched, err := f.operations.RecordReceipt(ctx, f.epoch, a.RandomID, 71, &at, time.Now())
	if err != nil || !matched {
		return model.SendAttemptResult{}, errors.New("synthetic receipt failed")
	}
	outcome := model.SendAttemptResult{Attempted: true, Receipt: &model.SendReceipt{Peer: a.Peer, RandomID: a.RandomID, MessageID: id, SentAt: at.Format(time.RFC3339)}}
	if f.afterReceipt != nil {
		if err := f.afterReceipt(ctx); err != nil {
			return outcome, model.TextError(model.ErrorCancelled, err)
		}
	}
	if f.postAck {
		outcome.FailureCode = model.SendFailureStateSyncFailed
		return outcome, model.TextError(model.ErrorStateSyncFailed, errors.New("hostile-upstream-marker"))
	}
	return outcome, nil
}
func sendingWireService(t *testing.T) (*sender.Service, *sendingWireBackend) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := privatefs.EnsureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dir, "metadata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repository, err := store.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveConfig(ctx, store.AccountConfig{APIID: 1, Environment: store.TestEnvironment, TestDC: 2, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	epoch := strings.Repeat("e", 43)
	if err := repository.RecordAuthorization(ctx, epoch, nil, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	policies, err := policy.New(db, filepath.Join(dir, "policy.lock"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := policies.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.SetFullSend(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	operations, err := store.NewOutgoingRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:42")
	backend := &sendingWireBackend{operations: operations, peer: peer, epoch: epoch}
	service, err := sender.New(backend, policies, operations, time.Now, rand.Reader, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return service, backend
}
func sendingSchemas(t *testing.T, session *mcp.ClientSession, ctx context.Context) map[string]any {
	t.Helper()
	inventory, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := make(map[string]any)
	for _, tool := range inventory.Tools {
		schemas[tool.Name] = tool.OutputSchema
	}
	return schemas
}
func TestSendingWorkflowOverStdioRelay(t *testing.T) {
	service, backend := sendingWireService(t)
	path, ctx := serveServicesTestServer(t, nil, service)
	session := sendingClient(t, path, ctx)
	schemas := sendingSchemas(t, session, ctx)
	resolved, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "resolve_recipient", Arguments: map[string]any{"username": "SyntheticRecipient"}})
	if err != nil || resolved.IsError {
		t.Fatalf("resolve: %v %v", err, resolved)
	}
	value := assertSendingMirror(t, resolved, schemas["resolve_recipient"])
	item := value["items"].([]any)[0].(map[string]any)
	args := map[string]any{"peer": item["peer"], "operation_id": item["operation_id"], "text": "Synthetic \nmessage 😀"}
	for range 2 {
		sent, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: args})
		if err != nil || sent.IsError {
			t.Fatalf("send: %v %v", err, sent)
		}
		value := assertSendingMirror(t, sent, schemas["send_message"])
		if value["items"].([]any)[0].(map[string]any)["state"] != "sent" {
			t.Fatal("send did not return receipt")
		}
	}
	if backend.sends.Load() != 1 {
		t.Fatal("same operation sent twice")
	}
	args["text"] = "Different synthetic text"
	conflict, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: args})
	if err != nil || !conflict.IsError {
		t.Fatal("operation conflict accepted", err)
	}
	value = assertSendingMirror(t, conflict, schemas["send_message"])
	if value["error"].(map[string]any)["category"] != "operation_conflict" || backend.sends.Load() != 1 {
		t.Fatal("conflict reached Telegram")
	}
	backend.notReady.Store(true)
	status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_send_status", Arguments: map[string]any{"operation_id": item["operation_id"]}})
	if err != nil || status.IsError {
		t.Fatal("offline status failed", err)
	}
	assertSendingMirror(t, status, schemas["get_send_status"])
}
func TestSendingKnownReceiptSurvivesErrorOverStdio(t *testing.T) {
	service, backend := sendingWireService(t)
	backend.postAck = true
	path, ctx := serveServicesTestServer(t, nil, service)
	session := sendingClient(t, path, ctx)
	schemas := sendingSchemas(t, session, ctx)
	opID, err := model.NewOperationID(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"peer": backend.peer.String(), "operation_id": opID, "text": "Synthetic acknowledgement"}})
	if err != nil || !result.IsError {
		t.Fatal("post-ack failure missing", err)
	}
	value := assertSendingMirror(t, result, schemas["send_message"])
	item := value["items"].([]any)[0].(map[string]any)
	if item["state"] != "sent" || item["message_id"] != "tgmsg:v1:chat:42:71" || item["failure_code"] != "state_sync_failed" {
		t.Fatal("error discarded known receipt")
	}
	if strings.Contains(result.Content[0].(*mcp.TextContent).Text, "hostile-upstream-marker") {
		t.Fatal("raw provider error leaked")
	}
	status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_send_status", Arguments: map[string]any{"operation_id": opID}})
	if err != nil || status.IsError {
		t.Fatal("fenced status unavailable", err)
	}
	assertSendingMirror(t, status, schemas["get_send_status"])
}
func TestSendingAbsoluteRetryAndUnavailableStatesOverStdio(t *testing.T) {
	service, backend := sendingWireService(t)
	retryAt := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	backend.resolveError = &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, RetryAt: retryAt, Cause: errors.New("hostile-provider-marker")}
	path, ctx := serveServicesTestServer(t, nil, service)
	session := sendingClient(t, path, ctx)
	schemas := sendingSchemas(t, session, ctx)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "resolve_recipient", Arguments: map[string]any{"phone": "+123456789"}})
	if err != nil || !result.IsError {
		t.Fatal("missing absolute wait", err)
	}
	value := assertSendingMirror(t, result, schemas["resolve_recipient"])
	failure := value["error"].(map[string]any)
	if failure["retry_at"] != retryAt.Format(time.RFC3339) || failure["retry_after_seconds"] != nil {
		t.Fatal("provider deadline was changed")
	}
	oldID, err := model.NewOperationID(time.Now().Add(-8*24*time.Hour), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_send_status", Arguments: map[string]any{"operation_id": oldID}})
	if err != nil || !status.IsError {
		t.Fatal("missing record invented a state", err)
	}
	value = assertSendingMirror(t, status, schemas["get_send_status"])
	if value["error"].(map[string]any)["category"] != "operation_unavailable" {
		t.Fatal("missing record is not unavailable")
	}
	expired, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"peer": backend.peer.String(), "operation_id": oldID, "text": "Synthetic expired send"}})
	if err != nil || !expired.IsError {
		t.Fatal("expired operation admitted", err)
	}
	value = assertSendingMirror(t, expired, schemas["send_message"])
	if value["error"].(map[string]any)["category"] != "operation_expired" || backend.sends.Load() != 0 {
		t.Fatal("expired operation reached Telegram")
	}
}

func TestSendingDisconnectAfterAcceptanceReplaysOnSecondClient(t *testing.T) {
	service, backend := sendingWireService(t)
	accepted := make(chan struct{})
	backend.afterReceipt = func(ctx context.Context) error { close(accepted); <-ctx.Done(); return ctx.Err() }
	path, ctx := serveServicesTestServer(t, nil, service)
	first, disconnect := sendingClientWithDisconnect(t, path, ctx)
	second := sendingClient(t, path, ctx)
	schemas := sendingSchemas(t, second, ctx)
	operationID, err := model.NewOperationID(time.Now(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"peer": backend.peer.String(), "operation_id": operationID, "text": "Synthetic interrupted reply"}
	firstDone := make(chan error, 1)
	go func() {
		_, err := first.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: args})
		firstDone <- err
	}()
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic send never reached acceptance")
	}
	disconnect()
	select {
	case <-firstDone:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnected client call did not stop")
	}
	var replay *mcp.CallToolResult
	deadline := time.Now().Add(2 * time.Second)
	for {
		replay, err = second.CallTool(ctx, &mcp.CallToolParams{Name: "send_message", Arguments: args})
		if err != nil {
			t.Fatal("second client transport failed", err)
		}
		if !replay.IsError {
			break
		}
		var value struct {
			Error struct {
				Category string `json:"category"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(replay.Content[0].(*mcp.TextContent).Text), &value); err != nil {
			t.Fatal(err)
		}
		if value.Error.Category != "not_ready" || !time.Now().Before(deadline) {
			t.Fatal("second client could not replay accepted receipt", replay)
		}
		time.Sleep(10 * time.Millisecond)
	}
	value := assertSendingMirror(t, replay, schemas["send_message"])
	operation := value["items"].([]any)[0].(map[string]any)
	if operation["state"] != "sent" || operation["message_id"] != "tgmsg:v1:chat:42:71" || backend.sends.Load() != 1 {
		t.Fatal("disconnect lost receipt or caused another send")
	}
}
