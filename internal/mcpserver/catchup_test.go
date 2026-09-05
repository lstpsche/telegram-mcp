package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestCatchUpValidatesInputsBeforeReadiness(t *testing.T) {
	valid := map[string]any{"scope": "tgscope:v1:0123456789abcdef0123456789abcdef", "since": "2026-09-05T12:00:00Z", "until": "2026-09-05T12:00:01Z"}
	for _, field := range []string{"scope", "since", "until", "limit", "cursor", "query"} {
		args := map[string]any{}
		for k, v := range valid {
			args[k] = v
		}
		args[field] = ""
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		result, err := callCatchUp(context.Background(), nil, "req_invalid", raw)
		if model.TextErrorCategory(err) != model.ErrorInvalidInput || len(result.JSON) != 0 {
			t.Fatalf("invalid %s accepted", field)
		}
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callCatchUp(context.Background(), nil, "req_ready", raw); model.TextErrorCategory(err) != model.ErrorNotReady {
		t.Fatal("unavailable service not reported")
	}
}
