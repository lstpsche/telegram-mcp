package sender

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

func prepare[T interface{ Validate() error }](requestID string, item T, now time.Time, state model.FreshnessState, resultErr error) (Result, error) {
	if err := item.Validate(); err != nil {
		return Result{}, model.TextError(model.ErrorInternal, errors.Join(resultErr, err))
	}
	freshness, err := model.NewFreshness(state, now)
	if err != nil {
		return Result{}, model.TextError(model.ErrorInternal, err)
	}
	envelope, err := model.NewEnvelope(requestID, freshness, []T{item})
	if err != nil {
		return Result{}, model.TextError(model.ErrorInternal, err)
	}
	if resultErr != nil {
		failure, err := sendingErrorEnvelope(requestID, resultErr)
		if err != nil {
			return Result{}, model.TextError(model.ErrorInternal, errors.Join(resultErr, err))
		}
		envelope.Error = &failure.Error
		if category := model.TextErrorCategory(resultErr); category == model.ErrorStateSyncFailed || category == model.ErrorReceiptNotPersisted {
			envelope.Warnings = append(envelope.Warnings, model.WarningFreshnessDegraded)
		}
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return Result{}, model.TextError(model.ErrorInternal, err)
	}
	return Result{JSON: encoded, IsError: resultErr != nil}, resultErr
}

func sendingErrorEnvelope(requestID string, err error) (model.ErrorEnvelope, error) {
	category := model.TextErrorCategory(err)
	if category == model.ErrorRateLimited {
		var failure *model.SendError
		if !errors.As(err, &failure) || failure.RetryAt.IsZero() {
			return model.NewErrorEnvelope(model.ErrorInternal, requestID, nil)
		}
		return model.NewRetryAtErrorEnvelope(requestID, failure.RetryAt)
	}
	return model.NewErrorEnvelope(category, requestID, nil)
}

func prepareError(requestID string, resultErr error) (Result, error) {
	envelope, err := sendingErrorEnvelope(requestID, resultErr)
	if err != nil {
		return Result{}, model.TextError(model.ErrorInternal, errors.Join(resultErr, err))
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return Result{}, model.TextError(model.ErrorInternal, errors.Join(resultErr, err))
	}
	return Result{JSON: encoded, IsError: true}, resultErr
}

// addResultError preserves the already-normalized operation facts. Building the
// fixed error through the public model prevents a provider cause entering JSON.
func addResultError(result Result, resultErr error) (Result, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(result.JSON, &fields); err != nil {
		return result, err
	}
	var requestID string
	if err := json.Unmarshal(fields["request_id"], &requestID); err != nil {
		return result, err
	}
	envelope, err := sendingErrorEnvelope(requestID, resultErr)
	if err != nil {
		return result, err
	}
	body, err := json.Marshal(envelope.Error)
	if err != nil {
		return result, err
	}
	fields["error"] = body
	encoded, err := json.Marshal(fields)
	if err != nil {
		return result, err
	}
	return Result{JSON: encoded, IsError: true}, nil
}

func outgoingError(err error) error {
	switch {
	case errors.Is(err, store.ErrOutgoingConflict):
		return model.TextError(model.ErrorOperationConflict, err)
	case errors.Is(err, store.ErrOutgoingExpired):
		return model.TextError(model.ErrorOperationExpired, err)
	case errors.Is(err, store.ErrOutgoingUnavailable):
		return model.TextError(model.ErrorOperationUnavailable, err)
	default:
		return safeError(err)
	}
}
