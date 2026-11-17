package sender

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestErrorEnvelopeRequiresRetryEvidenceAndHandlesMissingCause(t *testing.T) {
	retryAt := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for _, scenario := range []struct {
		name     string
		cause    error
		category model.ErrorCategory
		retryAt  string
	}{
		{name: "missing_cause", category: model.ErrorInternal},
		{name: "missing_typed_retry", cause: model.TextError(model.ErrorRateLimited, errors.New("sensitive provider diagnostic")), category: model.ErrorInternal},
		{name: "missing_deadline", cause: &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, Cause: errors.New("sensitive provider diagnostic")}, category: model.ErrorInternal},
		{name: "absolute_deadline", cause: &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, RetryAt: retryAt, Cause: errors.New("sensitive provider diagnostic")}, category: model.ErrorRateLimited, retryAt: retryAt.Format(time.RFC3339Nano)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			envelope, err := ErrorEnvelope("req_error", scenario.cause)
			if err != nil {
				t.Fatal(err)
			}
			if err := envelope.Validate(); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Category != scenario.category || envelope.Error.RetryAt != scenario.retryAt || envelope.Error.RetryAfterSeconds != nil {
				t.Fatalf("incorrect fixed error envelope: %+v", envelope.Error)
			}
			encoded, err := json.Marshal(envelope)
			if err != nil || strings.Contains(string(encoded), "sensitive") {
				t.Fatalf("unsafe serialized error: %v", err)
			}
		})
	}
}
