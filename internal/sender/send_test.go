package sender

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func (f *fixture) accept(t *testing.T, ctx context.Context, attempt model.SendAttempt) model.SendAttemptResult {
	t.Helper()
	operation, found, err := f.operations.Find(ctx, attempt.OperationID, f.now)
	if err != nil || !found || operation.State != model.SendInFlight || operation.RandomID != attempt.RandomID {
		t.Fatalf("send began before durable dispatch: found=%v state=%s error=%v", found, operation.State, err)
	}
	// This transaction proves the service holds no SQL transaction over I/O.
	confirmed, found, err := f.operations.RecordReceipt(ctx, f.epoch, attempt.RandomID, 82, &f.now, f.now)
	if err != nil || !found {
		t.Fatalf("receipt sink could not write under policy lease: %v", err)
	}
	return model.SendAttemptResult{Attempted: true, Receipt: &model.SendReceipt{Peer: attempt.Peer, RandomID: attempt.RandomID, MessageID: *confirmed.MessageID, SentAt: f.now.Format(time.RFC3339Nano)}}
}

func TestSendPersistsBeforeEffectAndExactReplayDoesNoIO(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "  Synthetic message\n🙂  ")
	f.backend.send = func(ctx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
		if attempt.Text != request.Text {
			t.Fatal("text bytes changed")
		}
		return f.accept(t, ctx, attempt), nil
	}
	result, err := f.service.SendMessage(context.Background(), "req_first", request)
	if err != nil || result.IsError {
		t.Fatalf("send: %v", err)
	}
	if operation := onlyOperation(t, result); operation.State != model.SendSent || operation.MessageID == nil {
		t.Fatal("receipt missing")
	}
	checks := f.backend.readyCalls
	f.backend.notReady = true
	result, err = f.service.SendMessage(context.Background(), "req_again", request)
	if err != nil || onlyOperation(t, result).State != model.SendSent {
		t.Fatalf("replay: %v", err)
	}
	if f.backend.sendCalls != 1 || f.backend.prepareCalls != 1 || f.backend.readyCalls != checks {
		t.Fatal("receipt replay performed I/O")
	}
	for _, alter := range []func(*model.SendRequest){
		func(r *model.SendRequest) { r.Text += " " },
		func(r *model.SendRequest) { r.Peer, _ = model.NewPeerID(model.PeerKindUser, 18) },
		func(r *model.SendRequest) { reply, _ := model.NewMessageID(f.peer, 1); r.ReplyTo = &reply },
	} {
		changed := request
		alter(&changed)
		if _, err := f.service.SendMessage(context.Background(), "req_conflict", changed); model.TextErrorCategory(err) != model.ErrorOperationConflict {
			t.Fatalf("conflict accepted: %v", err)
		}
	}
	if f.backend.readyCalls != checks {
		t.Fatal("conflict checked backend")
	}
}

func TestRejectedRetryKeepsActualRandomIDAndExactWait(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	retryAt := f.now.Add(90 * time.Minute)
	f.backend.prepare = func(context.Context, model.SendAttempt) error {
		if f.backend.prepareCalls == 1 {
			return &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, RetryAt: retryAt, Cause: errors.New("sensitive provider diagnostic")}
		}
		return nil
	}
	f.backend.send = func(ctx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
		return f.accept(t, ctx, attempt), nil
	}
	result, err := f.service.SendMessage(context.Background(), "req_wait", request)
	if model.TextErrorCategory(err) != model.ErrorRateLimited || !result.IsError {
		t.Fatalf("rate rejection: %v", err)
	}
	first := onlyOperation(t, result)
	if first.State != model.SendNotSent || first.RetryAt != retryAt.Format(time.RFC3339Nano) {
		t.Fatal("exact wait was lost")
	}
	var envelope model.Envelope[model.SendOperation]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.RetryAt != retryAt.Format(time.RFC3339Nano) || envelope.Error.RetryAfterSeconds != nil {
		t.Fatal("long provider wait was clamped or changed")
	}
	checks := f.backend.readyCalls
	if _, err := f.service.SendMessage(context.Background(), "req_still_waiting", request); model.TextErrorCategory(err) != model.ErrorRateLimited {
		t.Fatalf("cooldown: %v", err)
	}
	if f.backend.readyCalls != checks || f.backend.sendCalls != 0 {
		t.Fatal("waiting operation reached backend")
	}
	f.now = retryAt
	result, err = f.service.SendMessage(context.Background(), "req_retry", request)
	if err != nil || onlyOperation(t, result).State != model.SendSent {
		t.Fatalf("eligible retry: %v", err)
	}
	if len(f.backend.preparations) != 2 || f.backend.preparations[0].RandomID == 0 || f.backend.preparations[0].RandomID != f.backend.preparations[1].RandomID {
		t.Fatal("retry changed actual Telegram random ID")
	}
}

func TestAmbiguousAttemptCannotBecomeSafeRetry(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	f.backend.send = func(context.Context, model.SendAttempt) (model.SendAttemptResult, error) {
		return model.SendAttemptResult{Attempted: true}, errors.New("provider lost response")
	}
	result, err := f.service.SendMessage(context.Background(), "req_ambiguous", request)
	if model.TextErrorCategory(err) != model.ErrorSendEffectUncertain || onlyOperation(t, result).State != model.SendUncertain {
		t.Fatalf("ambiguity discarded: %v", err)
	}
	checks := f.backend.readyCalls
	f.backend.notReady = true
	f.backend.send = func(context.Context, model.SendAttempt) (model.SendAttemptResult, error) {
		t.Fatal("uncertain operation resent")
		return model.SendAttemptResult{}, nil
	}
	result, err = f.service.SendMessage(context.Background(), "req_replay", request)
	if model.TextErrorCategory(err) != model.ErrorSendEffectUncertain || onlyOperation(t, result).State != model.SendUncertain {
		t.Fatalf("uncertain replay: %v", err)
	}
	if f.backend.readyCalls != checks || f.backend.sendCalls != 1 {
		t.Fatal("uncertain replay used backend")
	}
	result, err = f.service.SendStatus(context.Background(), "req_status", request.OperationID)
	if err != nil || result.IsError || onlyOperation(t, result).State != model.SendUncertain {
		t.Fatalf("local uncertain status: %v", err)
	}
}

func TestAttemptedProviderRejectionKeepsReasonWithoutPermittingRetry(t *testing.T) {
	for _, code := range []model.SendFailureCode{model.SendFailureWriteForbidden, model.SendFailurePaymentRequired, model.SendFailureRateLimited, model.SendFailureAccountRestricted, model.SendFailureMessageTooLong} {
		t.Run(string(code), func(t *testing.T) {
			f := newFixture(t)
			f.permission(t, true, false)
			request := f.request(t, "Synthetic message")
			retryAt := time.Time{}
			if code == model.SendFailureRateLimited {
				retryAt = f.now.Add(48 * time.Hour)
			}
			f.backend.send = func(context.Context, model.SendAttempt) (model.SendAttemptResult, error) {
				return model.SendAttemptResult{Attempted: true, FailureCode: code, RetryAt: retryAt}, errors.New("provider rejection after opaque replay")
			}
			result, err := f.service.SendMessage(context.Background(), "req_rejected", request)
			operation := onlyOperation(t, result)
			if model.TextErrorCategory(err) != model.ErrorSendEffectUncertain || operation.State != model.SendUncertain || operation.FailureCode != code {
				t.Fatalf("attempted rejection was made retryable or lost reason: %v %s", err, result.JSON)
			}
			if !retryAt.IsZero() && operation.RetryAt != retryAt.Format(time.RFC3339Nano) {
				t.Fatal("uncertain operation lost exact provider wait")
			}
			if _, err := f.service.SendMessage(context.Background(), "req_again", request); model.TextErrorCategory(err) != model.ErrorSendEffectUncertain || f.backend.sendCalls != 1 {
				t.Fatalf("attempted rejection replayed: %v", err)
			}
		})
	}
}

func TestKnownReceiptSurvivesLaterErrorAndCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(map[bool]string{false: "update_failure", true: "caller_cancelled"}[cancelCaller], func(t *testing.T) {
			f := newFixture(t)
			f.permission(t, true, false)
			request := f.request(t, "Synthetic message")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.backend.send = func(callCtx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
				if cancelCaller {
					cancel()
				}
				metadataCtx, stop := context.WithTimeout(context.WithoutCancel(callCtx), metadataGrace)
				defer stop()
				outcome := f.accept(t, metadataCtx, attempt)
				outcome.FailureCode = model.SendFailureStateSyncFailed
				return outcome, errors.New("sensitive hook diagnostic")
			}
			result, err := f.service.SendMessage(ctx, "req_confirmed", request)
			operation := onlyOperation(t, result)
			if model.TextErrorCategory(err) != model.ErrorStateSyncFailed || !result.IsError || operation.State != model.SendSent || operation.MessageID == nil || operation.FailureCode != model.SendFailureStateSyncFailed {
				t.Fatalf("known receipt discarded: %v %s", err, result.JSON)
			}
			if strings.Contains(string(result.JSON), "sensitive") || strings.Contains(err.Error(), "sensitive") {
				t.Fatal("provider diagnostic leaked")
			}
			if f.service.Ready() {
				t.Fatal("synchronization failure did not fence effects")
			}
			result, err = f.service.SendStatus(context.Background(), "req_status", request.OperationID)
			if err != nil || onlyOperation(t, result).State != model.SendSent {
				t.Fatalf("durable receipt missing: %v", err)
			}
			result, err = f.service.SendMessage(context.Background(), "req_replay", request)
			if err != nil || onlyOperation(t, result).State != model.SendSent || f.backend.sendCalls != 1 {
				t.Fatalf("fenced receipt replay: %v", err)
			}
		})
	}
}

func TestPostAcceptanceCancellationKeepsReceiptAndFencesFreshSends(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		cause error
	}{
		{name: "cancelled", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFixture(t)
			f.permission(t, true, false)
			request := f.request(t, "Synthetic message")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.backend.send = func(callCtx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
				outcome := f.accept(t, callCtx, attempt)
				if scenario.cause == context.Canceled {
					cancel()
				}
				outcome.FailureCode = model.SendFailureStateSyncFailed
				return outcome, &model.SendError{Category: model.ErrorStateSyncFailed, FailureCode: model.SendFailureStateSyncFailed, Cause: scenario.cause}
			}
			result, err := f.service.SendMessage(ctx, "req_cancelled_sync", request)
			operation := onlyOperation(t, result)
			if !errors.Is(err, scenario.cause) || model.TextErrorCategory(err) != model.ErrorStateSyncFailed || !result.IsError || operation.State != model.SendSent || operation.MessageID == nil || operation.MessageID.TelegramID() != 82 || operation.SentAt != f.now.Format(time.RFC3339Nano) || operation.FailureCode != model.SendFailureStateSyncFailed {
				t.Fatalf("post-acceptance cancellation contradicted the receipt: %v %s", err, result.JSON)
			}
			if !f.backend.Ready() || f.service.Ready() {
				t.Fatal("degraded send did not retain its own effect fence")
			}
			for _, inspect := range []func() (Result, error){
				func() (Result, error) {
					return f.service.SendStatus(context.Background(), "req_local_status", request.OperationID)
				},
				func() (Result, error) {
					return f.service.SendMessage(context.Background(), "req_retained_receipt", request)
				},
			} {
				retained, err := inspect()
				if err != nil || retained.IsError || onlyOperation(t, retained).State != model.SendSent {
					t.Fatalf("post-acceptance cancellation hid retained facts: %v", err)
				}
			}
			_, err = f.service.SendMessage(context.Background(), "req_fenced_effect", f.request(t, "Another synthetic message"))
			if model.TextErrorCategory(err) != model.ErrorNotReady || f.backend.prepareCalls != 1 || f.backend.sendCalls != 1 {
				t.Fatalf("post-acceptance cancellation permitted another effect: %v", err)
			}
		})
	}
}

func TestKnownReceiptWithFailedSinkIsReturnedWithoutAnotherWrite(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	f.backend.send = func(ctx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
		// This sink failure is deliberately persistent, so another write would
		// hide the adapter's recording boundary or consume a second grace.
		if _, err := f.database.ExecContext(ctx, "CREATE TRIGGER reject_receipt BEFORE UPDATE ON outgoing_operations BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END"); err != nil {
			t.Fatal(err)
		}
		id, _ := model.NewMessageID(attempt.Peer, 82)
		return model.SendAttemptResult{Attempted: true, Receipt: &model.SendReceipt{Peer: attempt.Peer, RandomID: attempt.RandomID, MessageID: id}, FailureCode: model.SendFailureReceiptNotPersisted}, errors.New("sink failed")
	}
	result, err := f.service.SendMessage(context.Background(), "req_sink", request)
	if model.TextErrorCategory(err) != model.ErrorReceiptNotPersisted || onlyOperation(t, result).State != model.SendSent {
		t.Fatalf("receipt suppressed: %v", err)
	}
	if f.service.Ready() {
		t.Fatal("failed receipt persistence did not fence sends")
	}
	operation, found, err := f.operations.Find(context.Background(), request.OperationID, f.now)
	if err != nil || !found || operation.State != model.SendInFlight {
		t.Fatal("failed sink evidence was rewritten")
	}
}

func TestLaterRejectionCannotEraseIndependentlyRecordedReceipt(t *testing.T) {
	for _, code := range []model.SendFailureCode{model.SendFailureWriteForbidden, model.SendFailurePaymentRequired, model.SendFailureRateLimited, model.SendFailureReceiptNotPersisted} {
		t.Run(string(code), func(t *testing.T) {
			f := newFixture(t)
			f.permission(t, true, false)
			request := f.request(t, "Synthetic message")
			retryAt := time.Time{}
			if code == model.SendFailureRateLimited {
				retryAt = f.now.Add(48 * time.Hour)
			}
			f.backend.send = func(ctx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
				f.accept(t, ctx, attempt)
				if _, err := f.database.ExecContext(ctx, "CREATE TRIGGER preserve_sent_fact BEFORE UPDATE ON outgoing_operations BEGIN SELECT RAISE(ABORT,'synthetic immutable receipt'); END"); err != nil {
					t.Fatal(err)
				}
				return model.SendAttemptResult{Attempted: true, FailureCode: code, RetryAt: retryAt}, errors.New("sensitive later rejection")
			}
			result, err := f.service.SendMessage(context.Background(), "req_late_receipt", request)
			wantCategory, wantCode := model.ErrorStateSyncFailed, model.SendFailureStateSyncFailed
			if code == model.SendFailureReceiptNotPersisted {
				wantCategory, wantCode = model.ErrorReceiptNotPersisted, model.SendFailureReceiptNotPersisted
			}
			operation := onlyOperation(t, result)
			if model.TextErrorCategory(err) != wantCategory || !result.IsError || operation.State != model.SendSent || operation.MessageID == nil || operation.MessageID.TelegramID() != 82 || operation.FailureCode != wantCode || operation.RetryAt != "" {
				t.Fatalf("later error contradicted known receipt: %v %s", err, result.JSON)
			}
			var envelope model.Envelope[model.SendOperation]
			if err := json.Unmarshal(result.JSON, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error == nil || envelope.Error.Category != wantCategory || envelope.Error.RetryAt != "" || envelope.Error.RetryAfterSeconds != nil || len(envelope.Warnings) != 1 || envelope.Warnings[0] != model.WarningFreshnessDegraded {
				t.Fatal("confirmed send advertised a provider retry or lost degradation")
			}
			if strings.Contains(string(result.JSON), "sensitive") || f.service.Ready() {
				t.Fatal("later failure leaked its cause or left effects enabled")
			}
			retained, found, findErr := f.operations.Find(context.Background(), request.OperationID, f.now)
			if findErr != nil || !found || retained.State != model.SendSent || retained.MessageID == nil || retained.FailureCode != "" || retained.RetryAt != nil {
				t.Fatalf("known durable receipt was rewritten: %v", findErr)
			}
		})
	}
}

func TestLeaseSerializesSameIDAndRevocation(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	entered, release := make(chan struct{}), make(chan struct{})
	f.backend.send = func(ctx context.Context, attempt model.SendAttempt) (model.SendAttemptResult, error) {
		close(entered)
		<-release
		return f.accept(t, ctx, attempt), nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.service.SendMessage(context.Background(), "req_first", request); done <- err }()
	<-entered
	if _, err := f.service.SendMessage(context.Background(), "req_race", request); !errors.Is(err, policy.ErrBusy) {
		t.Fatalf("same-ID race did not respect policy lease: %v", err)
	}
	if lease, err := f.policy.Acquire(context.Background()); !errors.Is(err, policy.ErrBusy) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("revocation bypassed active effect: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.permission(t, false, false)
	if _, err := f.service.SendMessage(context.Background(), "req_revoked", request); model.TextErrorCategory(err) != model.ErrorPolicyDenied {
		t.Fatalf("revoked access reused receipt: %v", err)
	}
	if f.backend.sendCalls != 1 {
		t.Fatal("race or revoked call dispatched twice")
	}
}

func TestExpiredOperationsAndWindowEndNeverDispatch(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	f.now = f.now.Add(model.SendDispatchWindow)
	if _, err := f.service.SendMessage(context.Background(), "req_old", request); model.TextErrorCategory(err) != model.ErrorOperationExpired {
		t.Fatalf("expired key: %v", err)
	}
	if f.backend.readyCalls != 0 {
		t.Fatal("expired key reached backend")
	}
	request = f.request(t, "Synthetic message")
	f.backend.prepare = func(ctx context.Context, _ model.SendAttempt) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > OperationTimeout {
			t.Fatal("missing bounded operation deadline")
		}
		f.now = f.now.Add(model.SendDispatchWindow)
		return nil
	}
	result, err := f.service.SendMessage(context.Background(), "req_elapsed", request)
	if model.TextErrorCategory(err) != model.ErrorOperationExpired || onlyOperation(t, result).State != model.SendNotSent || f.backend.sendCalls != 0 {
		t.Fatalf("elapsed window dispatched: %v", err)
	}
}

func TestCancellationBeforeDispatchIsNotSent(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	request := f.request(t, "Synthetic message")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.backend.prepare = func(context.Context, model.SendAttempt) error { cancel(); return nil }
	result, err := f.service.SendMessage(ctx, "req_cancel", request)
	if model.TextErrorCategory(err) != model.ErrorCancelled || onlyOperation(t, result).State != model.SendNotSent || f.backend.sendCalls != 0 {
		t.Fatalf("pre-dispatch cancellation: %v", err)
	}
	if operation, found, err := f.operations.Find(context.Background(), request.OperationID, f.now); err != nil || !found || operation.State != model.SendNotSent {
		t.Fatal("cancelled metadata not durably recorded")
	}
}

func TestOutgoingStorageAndErrorsExcludeBodiesAndAliases(t *testing.T) {
	f := newFixture(t)
	f.permission(t, true, false)
	const body = "SYNTHETIC_PRIVATE_BODY_abc123"
	const username = "Synthetic_Recipient"
	const phone = "+12025550128"
	f.backend.resolve = func(context.Context, model.ResolveRecipientQuery) (model.ResolvedRecipient, error) {
		return model.ResolvedRecipient{Peer: f.peer, Kind: f.peer.Kind(), Username: username, DisplayName: "Synthetic name"}, nil
	}
	for _, query := range []model.ResolveRecipientQuery{{Username: username}, {Phone: phone}} {
		if _, err := f.service.ResolveRecipient(context.Background(), "req_alias", query); err != nil {
			t.Fatal(err)
		}
	}
	f.backend.send = func(context.Context, model.SendAttempt) (model.SendAttemptResult, error) {
		return model.SendAttemptResult{Attempted: true}, errors.New(body + username + phone)
	}
	result, err := f.service.SendMessage(context.Background(), "req_sensitive", f.request(t, body))
	if err == nil {
		t.Fatal("ambiguous effect accepted")
	}
	for _, sensitive := range []string{body, username, phone} {
		if strings.Contains(err.Error(), sensitive) || strings.Contains(string(result.JSON), sensitive) {
			t.Fatal("sensitive error content leaked")
		}
	}
	rows, err := f.database.Query("SELECT operation_id,authorization_epoch,peer,reply_to,payload_digest,random_id,state,message_id,sent_at,failure_code,retry_at,created_at,updated_at,retain_until FROM outgoing_operations")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]any, 14)
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			var text string
			switch value := value.(type) {
			case string:
				text = value
			case []byte:
				text = string(value)
			}
			for _, sensitive := range []string{body, username, phone} {
				if strings.Contains(text, sensitive) {
					t.Fatal("sensitive data entered outgoing SQL")
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
