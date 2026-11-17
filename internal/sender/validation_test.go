package sender

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestRecipientNormalizationRejectsUnmatchedAndInvalidBackendResults(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	other, _ := model.NewPeerID(model.PeerKindUser, 27)
	id, _ := model.NewOperationID(f.now, strings.NewReader(strings.Repeat("x", 10)))
	for _, recipient := range []model.ResolvedRecipient{
		{Peer: other, Kind: other.Kind()},
		{Peer: f.peer, Kind: model.PeerKindChat},
		{Peer: f.peer, Kind: f.peer.Kind(), DisplayName: "synthetic\ncontrol"},
		{Peer: f.peer, Kind: f.peer.Kind(), OperationID: id},
	} {
		f.backend.resolve = func(context.Context, model.ResolveRecipientQuery) (model.ResolvedRecipient, error) {
			return recipient, nil
		}
		result, err := f.service.ResolveRecipient(context.Background(), "req_invalid", model.ResolveRecipientQuery{Peer: f.peer})
		if model.TextErrorCategory(err) != model.ErrorRecipientUnavailable || len(result.JSON) != 0 {
			t.Fatalf("invalid backend recipient released: %v", err)
		}
	}
}

func TestResolveAndPreflightWaitsKeepAbsoluteDeadline(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	retryAt := f.now.Add(26 * time.Hour)
	failure := &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, RetryAt: retryAt, Cause: errors.New("sensitive provider diagnostic")}
	f.backend.resolve = func(context.Context, model.ResolveRecipientQuery) (model.ResolvedRecipient, error) {
		return model.ResolvedRecipient{}, failure
	}
	result, err := f.service.ResolveRecipient(context.Background(), "req_resolve_wait", model.ResolveRecipientQuery{Phone: "+12025550128"})
	if model.TextErrorCategory(err) != model.ErrorRateLimited || !result.IsError {
		t.Fatalf("resolve wait: %v", err)
	}
	var envelope model.ErrorEnvelope
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.RetryAt != retryAt.Format(time.RFC3339Nano) || envelope.Error.RetryAfterSeconds != nil {
		t.Fatal("resolver lost absolute wait")
	}
	f.backend.prepare = func(context.Context, model.SendAttempt) error { return failure }
	result, err = f.service.SendMessage(context.Background(), "req_preflight_wait", f.request(t, "Synthetic message"))
	if model.TextErrorCategory(err) != model.ErrorRateLimited || onlyOperation(t, result).RetryAt != retryAt.Format(time.RFC3339Nano) || f.backend.sendCalls != 0 {
		t.Fatalf("preflight wait: %v", err)
	}
}

func TestNilPreparedActionCannotCrossDispatchBarrier(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	f.backend.nilPrepared = true
	request := f.request(t, "Synthetic message")
	result, err := f.service.SendMessage(context.Background(), "req_nil_action", request)
	if model.TextErrorCategory(err) != model.ErrorInternal || onlyOperation(t, result).State != model.SendNotSent || f.backend.sendCalls != 0 {
		t.Fatalf("nil preparation: %v", err)
	}
	if f.service.Ready() {
		t.Fatal("invalid prepared backend remained effect-ready")
	}
}

func TestMalformedReceiptRemainsUncertainAndFencesEffects(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	f.backend.send = func(_ context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
		id, _ := model.NewMessageID(attempt.Peer, 82)
		return model.SendAttemptResult{Attempted: true, Receipt: &model.SendReceipt{Peer: attempt.Peer, RandomID: attempt.RandomID + 1, MessageID: id}}, nil
	}
	result, err := f.service.SendMessage(context.Background(), "req_malformed", f.request(t, "Synthetic message"))
	if model.TextErrorCategory(err) != model.ErrorSendEffectUncertain || onlyOperation(t, result).State != model.SendUncertain || f.service.Ready() {
		t.Fatalf("unmatched receipt established a fact: %v", err)
	}
}

func TestNearWindowEndCapsNetworkDeadline(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	f.now = f.now.Add(model.SendDispatchWindow - time.Second)
	f.backend.prepare = func(ctx context.Context, _ model.SendAttempt) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatal("network deadline outlived retry window")
		}
		return model.TextError(model.ErrorRecipientUnavailable, nil)
	}
	result, err := f.service.SendMessage(context.Background(), "req_window", request)
	if model.TextErrorCategory(err) != model.ErrorRecipientUnavailable || onlyOperation(t, result).State != model.SendNotSent {
		t.Fatalf("near-window preflight: %v", err)
	}
}

func TestPostDispatchRecordingFailureFencesEffectsAndKeepsConservativeEvidence(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	f.backend.send = func(ctx context.Context, _ model.SendAttempt) (model.SendAttemptResult, error) {
		if _, err := f.database.ExecContext(ctx, "CREATE TRIGGER reject_outcome BEFORE UPDATE ON outgoing_operations BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END"); err != nil {
			t.Fatal(err)
		}
		return model.SendAttemptResult{Attempted: true}, errors.New("response unavailable")
	}
	result, err := f.service.SendMessage(context.Background(), "req_record_failure", request)
	if model.TextErrorCategory(err) != model.ErrorInternal || onlyOperation(t, result).State != model.SendUncertain || f.service.Ready() {
		t.Fatalf("recording failure was hidden: %v", err)
	}
	operation, found, err := f.operations.Find(context.Background(), request.OperationID, f.now)
	if err != nil || !found || operation.State != model.SendInFlight {
		t.Fatal("durable dispatch evidence was erased")
	}
	checks := f.backend.sendCalls
	if _, err := f.service.SendMessage(context.Background(), "req_another", f.request(t, "Another synthetic message")); model.TextErrorCategory(err) != model.ErrorNotReady || f.backend.sendCalls != checks {
		t.Fatalf("failed recording did not fence subsequent effects: %v", err)
	}
}

func TestAdapterMetadataFailureDoesNotStartAnotherWriteGrace(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(map[bool]string{false: "preflight", true: "dispatch"}[dispatched], func(t *testing.T) {
			f := newFixture(t)
			f.permission(t, true, false)
			request := f.request(t, "Synthetic message")
			retryAt := f.now.Add(time.Hour)
			failure := &model.SendError{Category: model.ErrorFreshnessDegraded, Cause: errors.Join(errors.New("synthetic adapter write failed"), model.ErrSendMetadataRecording)}
			installProbe := func(ctx context.Context) {
				t.Helper()
				if _, err := f.database.ExecContext(ctx, "CREATE TABLE outgoing_write_probe (calls INTEGER NOT NULL)"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.database.ExecContext(ctx, "INSERT INTO outgoing_write_probe VALUES (0)"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.database.ExecContext(ctx, "CREATE TRIGGER outgoing_write_probe_update AFTER UPDATE ON outgoing_operations BEGIN UPDATE outgoing_write_probe SET calls=calls+1; END"); err != nil {
					t.Fatal(err)
				}
			}
			if dispatched {
				f.backend.send = func(ctx context.Context, _ model.SendAttempt) (model.SendAttemptResult, error) {
					installProbe(ctx)
					return model.SendAttemptResult{Attempted: true, Uncertain: true, FailureCode: model.SendFailureRateLimited, RetryAt: retryAt}, failure
				}
			} else {
				f.backend.prepare = func(ctx context.Context, _ model.SendAttempt) error { installProbe(ctx); return failure }
			}
			result, err := f.service.SendMessage(context.Background(), "req_adapter_write_failure", request)
			operation := onlyOperation(t, result)
			if !errors.Is(err, model.ErrSendMetadataRecording) || !result.IsError || f.service.Ready() {
				t.Fatalf("metadata failure was lost or did not fence: %v", err)
			}
			wantState, wantDurable, wantCategory := model.SendNotSent, model.SendNotSent, model.ErrorFreshnessDegraded
			if dispatched {
				wantState, wantDurable, wantCategory = model.SendUncertain, model.SendInFlight, model.ErrorSendEffectUncertain
			}
			if operation.State != wantState || model.TextErrorCategory(err) != wantCategory || operation.FailureCode == model.SendFailureReceiptNotPersisted {
				t.Fatalf("metadata failure fabricated outcome: %v %s", err, result.JSON)
			}
			var writes int
			if err := f.database.QueryRow("SELECT calls FROM outgoing_write_probe").Scan(&writes); err != nil {
				t.Fatal(err)
			}
			if writes != 0 {
				t.Fatalf("adapter failure initiated %d additional metadata writes", writes)
			}
			retained, found, err := f.operations.Find(context.Background(), request.OperationID, f.now)
			if err != nil || !found || retained.State != wantDurable {
				t.Fatalf("durable dispatch evidence changed: %v", err)
			}
		})
	}
}

func TestAdapterMetadataDeadlineCapsRemainingSenderGrace(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(map[bool]string{false: "preflight", true: "dispatch"}[dispatched], func(t *testing.T) {
			f := newFixture(t)
			f.permission(t, true, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			retryAt := f.now.Add(time.Hour)
			var release func()
			blockMetadata := func(callCtx context.Context) error {
				t.Helper()
				// Hold the single metadata connection after the adapter has used
				// most of its grace; only the supplied remainder may be consumed.
				tx, err := f.database.BeginTx(callCtx, nil)
				if err != nil {
					t.Fatal(err)
				}
				release = func() { _ = tx.Rollback() }
				cancel()
				return &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, RetryAt: retryAt, MetadataDeadline: time.Now().Add(75 * time.Millisecond)}
			}
			if dispatched {
				f.backend.send = func(callCtx context.Context, _ model.SendAttempt) (model.SendAttemptResult, error) {
					return model.SendAttemptResult{Attempted: true, Uncertain: true, FailureCode: model.SendFailureRateLimited, RetryAt: retryAt}, blockMetadata(context.WithoutCancel(callCtx))
				}
			} else {
				f.backend.prepare = func(callCtx context.Context, _ model.SendAttempt) error {
					return blockMetadata(context.WithoutCancel(callCtx))
				}
			}
			started := time.Now()
			result, err := f.service.SendMessage(ctx, "req_remaining_grace", f.request(t, "Synthetic message"))
			elapsed := time.Since(started)
			if release != nil {
				release()
			}
			if model.TextErrorCategory(err) != model.ErrorInternal || !errors.Is(err, context.DeadlineExceeded) || f.service.Ready() {
				t.Fatalf("remaining metadata deadline not enforced: %v", err)
			}
			if elapsed > time.Second {
				t.Fatalf("sender restarted metadata grace: %s", elapsed)
			}
			wantState := model.SendNotSent
			if dispatched {
				wantState = model.SendUncertain
			}
			if onlyOperation(t, result).State != wantState {
				t.Fatal("bounded recording failure lost conservative outcome")
			}
		})
	}
}

func TestFailureErrorCarriesNearestNonzeroMetadataDeadline(t *testing.T) {
	inner := time.Now().Add(time.Second)
	outer := inner.Add(time.Second)
	root := &model.SendError{Category: model.ErrorRateLimited, MetadataDeadline: inner}
	for _, scenario := range []struct {
		name  string
		cause error
		want  time.Time
	}{
		{name: "wrapped", cause: model.TextError(model.ErrorInternal, root), want: inner},
		{name: "zero_outer", cause: &model.SendError{Category: model.ErrorInternal, Cause: root}, want: inner},
		{name: "nearest", cause: &model.SendError{Category: model.ErrorInternal, MetadataDeadline: outer, Cause: root}, want: outer},
		{name: "joined", cause: errors.Join(errors.New("synthetic diagnostic"), root), want: inner},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			failure := failureError(model.SendFailureEffectUncertain, time.Time{}, scenario.cause)
			if !failure.MetadataDeadline.Equal(scenario.want) {
				t.Fatal("metadata deadline was reset or lost")
			}
		})
	}
}

func TestPrunedAndOldEpochOperationNeverDispatch(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	f.admit(t, request)
	if _, err := f.database.Exec("UPDATE outgoing_operations SET authorization_epoch=?", strings.Repeat("x", 43)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SendMessage(context.Background(), "req_epoch", request); model.TextErrorCategory(err) != model.ErrorOperationUnavailable {
		t.Fatalf("old epoch rebound operation: %v", err)
	}
	f.now = f.now.Add(model.SendRetention)
	if _, err := f.service.SendStatus(context.Background(), "req_pruned", request.OperationID); model.TextErrorCategory(err) != model.ErrorOperationUnavailable {
		t.Fatalf("pruned status: %v", err)
	}
	if _, err := f.service.SendMessage(context.Background(), "req_old", request); model.TextErrorCategory(err) != model.ErrorOperationExpired {
		t.Fatalf("old key re-admitted: %v", err)
	}
	if f.backend.readyCalls != 0 || f.backend.prepareCalls != 0 || f.backend.sendCalls != 0 {
		t.Fatal("expired or wrong-epoch operation reached backend")
	}
}
