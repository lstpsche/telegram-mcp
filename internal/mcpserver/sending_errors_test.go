package mcpserver

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/sender"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSendingErrorEnvelopesKeepCanonicalRetryRulesOverStdio(t *testing.T) {
	retryAt := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	for _, scenario := range []struct {
		name     string
		cause    error
		category model.ErrorCategory
		retryAt  string
	}{
		{name: "missing_deadline", cause: &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, Cause: errors.New("sensitive provider diagnostic")}, category: model.ErrorInternal},
		{name: "exact_long_deadline", cause: &model.SendError{Category: model.ErrorRateLimited, FailureCode: model.SendFailureRateLimited, RetryAt: retryAt, Cause: errors.New("sensitive provider diagnostic")}, category: model.ErrorRateLimited, retryAt: retryAt.Format(time.RFC3339Nano)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, backend := sendingWireService(t)
			backend.resolveError = scenario.cause
			path, ctx := serveServicesTestServer(t, nil, service)
			session := sendingClient(t, path, ctx)
			schemas := sendingSchemas(t, session, ctx)
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "resolve_recipient", Arguments: map[string]any{"phone": "+12025550128"}})
			if err != nil || !result.IsError {
				t.Fatalf("missing fixed sending error: %v", err)
			}
			value := assertSendingMirror(t, result, schemas["resolve_recipient"])
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var envelope model.ErrorEnvelope
			if err := json.Unmarshal(encoded, &envelope); err != nil {
				t.Fatal(err)
			}
			if err := envelope.Validate(); err != nil {
				t.Fatal(err)
			}
			canonical, err := sender.ErrorEnvelope(envelope.RequestID, scenario.cause)
			if err != nil {
				t.Fatal(err)
			}
			if envelope.Error != canonical.Error || envelope.Error.Category != scenario.category || envelope.Error.RetryAt != scenario.retryAt || envelope.Error.RetryAfterSeconds != nil {
				t.Fatalf("sending error diverged from canonical envelope: %+v", envelope.Error)
			}
			if strings.Contains(string(encoded), "sensitive") || strings.Contains(string(encoded), "+12025550128") {
				t.Fatal("sending error leaked transient input or provider cause")
			}
		})
	}
}
