package sender

import (
	"context"
	"errors"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func (s *Service) SendMessage(ctx context.Context, requestID string, request model.SendRequest) (result Result, resultErr error) {
	if s == nil {
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	if !model.IsValidRequestID(requestID) {
		return Result{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	lease, err := s.policy.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		result, resultErr = closeLease(lease, result, resultErr)
	}()
	if err := requireSend(ctx, lease); err != nil {
		return Result{}, err
	}
	epoch, _, err := lease.Binding(ctx)
	if err != nil {
		return Result{}, err
	}
	digest := s.payloadDigest(request)
	operation, found, err := s.operations.Find(ctx, request.OperationID, s.now())
	if err != nil {
		return Result{}, outgoingError(err)
	}
	if found {
		if operation.AuthorizationEpoch != epoch {
			return Result{}, model.TextError(model.ErrorOperationUnavailable, nil)
		}
		if !samePayload(operation, request, digest) {
			return Result{}, model.TextError(model.ErrorOperationConflict, nil)
		}
		if operation.State != model.SendNotSent {
			return s.retained(requestID, operation)
		}
	}
	if err := model.ValidateOperationWindow(request.OperationID, s.now()); err != nil {
		return Result{}, model.TextError(model.ErrorOperationExpired, err)
	}
	if found && operation.RetryAt != nil && s.now().Before(*operation.RetryAt) {
		return prepare(requestID, operation.Public(), s.now(), model.FreshnessUnavailable, failureError(model.SendFailureRateLimited, *operation.RetryAt, nil))
	}
	if !s.Ready() {
		if found {
			return prepare(requestID, operation.Public(), s.now(), model.FreshnessUnavailable, model.TextError(model.ErrorNotReady, nil))
		}
		return Result{}, model.TextError(model.ErrorNotReady, nil)
	}
	created, _ := model.ParseOperationID(request.OperationID)
	ctx, stop := context.WithTimeout(ctx, created.Add(model.SendDispatchWindow).Sub(s.now()))
	defer stop()
	if !found {
		randomID, err := newRandomID(s.random)
		if err != nil {
			return Result{}, err
		}
		operation, _, err = s.operations.Admit(ctx, store.OutgoingOperation{
			OperationID: request.OperationID, AuthorizationEpoch: epoch, Peer: request.Peer,
			ReplyTo: request.ReplyTo, PayloadDigest: digest, RandomID: randomID,
		}, s.now())
		if err != nil {
			return Result{}, outgoingError(err)
		}
		if operation.State != model.SendNotSent {
			return s.retained(requestID, operation)
		}
	}
	attempt := model.SendAttempt{OperationID: request.OperationID, Peer: request.Peer, Text: request.Text, ReplyTo: request.ReplyTo, RandomID: operation.RandomID}
	prepared, err := s.backend.PrepareSend(ctx, attempt)
	if err != nil {
		code, retryAt := failureDetails(err)
		return s.recordResult(ctx, requestID, operation, false, code, retryAt, safeError(err))
	}
	if prepared == nil {
		s.fenced.Store(true)
		return s.recordResult(ctx, requestID, operation, false, "", time.Time{}, model.TextError(model.ErrorInternal, nil))
	}
	if err := requireSend(ctx, lease); err != nil {
		return s.recordResult(ctx, requestID, operation, false, "", time.Time{}, err)
	}
	if err := s.checkDispatch(ctx, request.OperationID); err != nil {
		return s.recordResult(ctx, requestID, operation, false, "", time.Time{}, err)
	}
	operation, dispatch, err := s.operations.BeginDispatch(ctx, request.OperationID, epoch, s.now())
	if err != nil {
		if errors.Is(err, store.ErrOutgoingWait) && operation.RetryAt != nil {
			return prepare(requestID, operation.Public(), s.now(), model.FreshnessUnavailable, failureError(model.SendFailureRateLimited, *operation.RetryAt, err))
		}
		if !errors.Is(err, store.ErrOutgoingExpired) && !errors.Is(err, store.ErrOutgoingUnavailable) && !errors.Is(err, store.ErrOutgoingConflict) {
			s.fenced.Store(true)
		}
		return Result{}, outgoingError(err)
	}
	if !dispatch {
		return s.retained(requestID, operation)
	}
	// Keep the final effect-deadline check adjacent to the prepared send.
	if err := s.checkDispatch(ctx, request.OperationID); err != nil {
		return s.recordResult(ctx, requestID, operation, false, "", time.Time{}, err)
	}
	outcome, sendErr := prepared(ctx)
	if outcome.Receipt != nil {
		return s.acknowledged(ctx, requestID, operation, outcome, sendErr)
	}
	uncertain := outcome.Uncertain || outcome.FailureCode == model.SendFailureEffectUncertain || outcome.FailureCode == model.SendFailureStateSyncFailed || outcome.FailureCode == model.SendFailureReceiptNotPersisted
	if outcome.Attempted {
		uncertain = true
	}
	if sendErr == nil {
		// A successful call must carry a receipt; absence proves no outcome.
		uncertain = true
		s.fenced.Store(true)
		sendErr = model.TextError(model.ErrorSendEffectUncertain, nil)
	}
	code := outcome.FailureCode
	if uncertain && code == "" {
		code = model.SendFailureEffectUncertain
	}
	if code == model.SendFailureRateLimited && outcome.RetryAt.IsZero() {
		uncertain, code = true, model.SendFailureEffectUncertain
	}
	if outcome.FailureCode == model.SendFailureStateSyncFailed || outcome.FailureCode == model.SendFailureReceiptNotPersisted {
		s.fenced.Store(true)
	}
	failure := failureError(code, outcome.RetryAt, sendErr)
	if uncertain {
		failure.Category = model.ErrorSendEffectUncertain
	}
	return s.recordResult(ctx, requestID, operation, uncertain, code, outcome.RetryAt, failure)
}

func (s *Service) checkDispatch(ctx context.Context, operationID string) error {
	if err := ctx.Err(); err != nil {
		return model.TextError(model.ErrorCancelled, err)
	}
	if err := model.ValidateOperationWindow(operationID, s.now()); err != nil {
		return model.TextError(model.ErrorOperationExpired, err)
	}
	if !s.Ready() {
		return model.TextError(model.ErrorNotReady, nil)
	}
	return nil
}

func (s *Service) retained(requestID string, operation store.OutgoingOperation) (Result, error) {
	var err error
	switch operation.State {
	case model.SendUncertain:
		err = failureError(model.SendFailureEffectUncertain, time.Time{}, nil)
	case model.SendInFlight:
		err = model.TextError(model.ErrorNotReady, nil)
	}
	return prepare(requestID, operation.Public(), s.now(), model.FreshnessUnavailable, err)
}

// recordResult uses a cancellation-independent context only for local metadata.
// No backend method is reachable from the cleanup grace period.
func (s *Service) recordResult(ctx context.Context, requestID string, operation store.OutgoingOperation, uncertain bool, code model.SendFailureCode, retryAt time.Time, resultErr error) (Result, error) {
	var retry *time.Time
	if code == model.SendFailureRateLimited && !retryAt.IsZero() {
		retry = &retryAt
	}
	if errors.Is(resultErr, model.ErrSendMetadataRecording) {
		s.fenced.Store(true)
		operation.State, operation.FailureCode, operation.RetryAt = model.SendNotSent, code, retry
		if uncertain {
			operation.State = model.SendUncertain
		}
		return prepare(requestID, operation.Public(), s.now(), model.FreshnessUnavailable, resultErr)
	}
	deadline := time.Now().Add(metadataGrace)
	if shared := metadataDeadline(resultErr); !shared.IsZero() && shared.Before(deadline) {
		deadline = shared
	}
	metadataCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	var recorded store.OutgoingOperation
	var err error
	if uncertain {
		recorded, err = s.operations.RecordUncertain(metadataCtx, operation.OperationID, operation.AuthorizationEpoch, code, retry, s.now())
	} else {
		recorded, err = s.operations.RecordNotSent(metadataCtx, operation.OperationID, operation.AuthorizationEpoch, code, retry, s.now())
	}
	if err != nil {
		s.fenced.Store(true)
		operation.State, operation.FailureCode, operation.RetryAt = model.SendNotSent, code, retry
		if uncertain {
			operation.State = model.SendUncertain
		}
		return prepare(requestID, operation.Public(), s.now(), model.FreshnessUnavailable, model.TextError(model.ErrorInternal, errors.Join(resultErr, err)))
	}
	if uncertain && recorded.State == model.SendSent {
		s.fenced.Store(true)
		if code == "" || code == model.SendFailureEffectUncertain {
			code = model.SendFailureStateSyncFailed
		}
		resultErr = failureError(code, retryAt, resultErr)
	}
	return prepare(requestID, recorded.Public(), s.now(), model.FreshnessUnavailable, resultErr)
}

func (s *Service) acknowledged(ctx context.Context, requestID string, operation store.OutgoingOperation, outcome model.SendAttemptResult, sendErr error) (Result, error) {
	receipt := outcome.Receipt
	if err := receipt.Validate(); err != nil || receipt.Peer != operation.Peer || receipt.RandomID != operation.RandomID {
		s.fenced.Store(true)
		return s.recordResult(ctx, requestID, operation, true, model.SendFailureEffectUncertain, time.Time{}, model.TextError(model.ErrorSendEffectUncertain, errors.Join(sendErr, err)))
	}
	confirmed := operation.Public()
	confirmed.State, confirmed.MessageID, confirmed.SentAt, confirmed.RetryAt = model.SendSent, &receipt.MessageID, receipt.SentAt, ""
	confirmed.FailureCode = outcome.FailureCode
	freshness := model.FreshnessLive
	if sendErr != nil || outcome.FailureCode != "" {
		s.fenced.Store(true)
		freshness = model.FreshnessUnavailable
		if confirmed.FailureCode != model.SendFailureReceiptNotPersisted {
			confirmed.FailureCode = model.SendFailureStateSyncFailed
		}
		sendErr = failureError(confirmed.FailureCode, time.Time{}, sendErr)
	}
	// The adapter has already attempted receipt persistence before update hooks.
	// A failed sink must not acquire another five-second write grace here.
	return prepare(requestID, confirmed, s.now(), freshness, sendErr)
}

func failureDetails(err error) (model.SendFailureCode, time.Time) {
	var failure *model.SendError
	if errors.As(err, &failure) {
		return failure.FailureCode, failure.RetryAt
	}
	switch model.TextErrorCategory(err) {
	case model.ErrorRecipientUnavailable:
		return model.SendFailureRecipientUnavailable, time.Time{}
	case model.ErrorInvalidReference:
		return model.SendFailureInvalidReference, time.Time{}
	case model.ErrorWriteForbidden:
		return model.SendFailureWriteForbidden, time.Time{}
	case model.ErrorPaymentRequired:
		return model.SendFailurePaymentRequired, time.Time{}
	case model.ErrorAccountRestricted:
		return model.SendFailureAccountRestricted, time.Time{}
	}
	return "", time.Time{}
}

func failureError(code model.SendFailureCode, retryAt time.Time, cause error) *model.SendError {
	category := model.TextErrorCategory(cause)
	switch code {
	case model.SendFailureWriteForbidden:
		category = model.ErrorWriteForbidden
	case model.SendFailurePaymentRequired:
		category = model.ErrorPaymentRequired
	case model.SendFailureRateLimited:
		category = model.ErrorRateLimited
	case model.SendFailureAccountRestricted:
		category = model.ErrorAccountRestricted
	case model.SendFailureEffectUncertain:
		category = model.ErrorSendEffectUncertain
	case model.SendFailureStateSyncFailed:
		category = model.ErrorStateSyncFailed
	case model.SendFailureReceiptNotPersisted:
		category = model.ErrorReceiptNotPersisted
	case model.SendFailureRecipientUnavailable:
		category = model.ErrorRecipientUnavailable
	case model.SendFailureInvalidReference:
		category = model.ErrorInvalidReference
	}
	if !category.IsValid() {
		category = model.ErrorInternal
	}
	return &model.SendError{Category: category, FailureCode: code, RetryAt: retryAt, MetadataDeadline: metadataDeadline(cause), Cause: cause}
}

func metadataDeadline(err error) time.Time {
	if failure, ok := err.(*model.SendError); ok && !failure.MetadataDeadline.IsZero() {
		return failure.MetadataDeadline
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, cause := range wrapped.Unwrap() {
			if deadline := metadataDeadline(cause); !deadline.IsZero() {
				return deadline
			}
		}
	case interface{ Unwrap() error }:
		return metadataDeadline(wrapped.Unwrap())
	}
	return time.Time{}
}
