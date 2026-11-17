package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type retainedReadWaitBackend struct{ *wireBackend }

func (b *retainedReadWaitBackend) History(context.Context, model.HistoryQuery) ([]model.Candidate, error) {
	b.fetches.Add(1)
	return nil, model.TextError(model.ErrorTelegramUnavailable, errors.New("private provider wait context"))
}

func TestRetainedReadWaitReturnsAVersionedSanitizedToolError(t *testing.T) {
	_, base := wireService(t)
	service, err := reader.New(&retainedReadWaitBackend{base}, base.repository, time.Now, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	path, ctx := serveTextTestServer(t, service)
	session := sendingClient(t, path, ctx)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_messages", Arguments: map[string]any{"peer": base.peer.String()}})
	if err != nil || result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("read wait did not return a tool error: result=%+v err=%v", result, err)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("read wait error was not JSON text")
	}
	envelope, err := model.DecodeStrict[model.ErrorEnvelope]([]byte(content.Text))
	if err != nil {
		t.Fatal("read wait error lost the versioned envelope", err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Category != model.ErrorTelegramUnavailable || envelope.Error.RetryAfterSeconds != nil || envelope.Error.RetryAt != "" {
		t.Fatal("read availability error changed its existing shape", content.Text)
	}
	if strings.Contains(content.Text, "private provider") || strings.Contains(content.Text, base.peer.String()) {
		t.Fatal("read error leaked private context", content.Text)
	}
	if base.acks.Load() != 0 {
		t.Fatal("failed read acknowledged history")
	}
}
