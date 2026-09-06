package mcpserver

import (
	"io"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAllToolsRespectReadinessOverStdioRelay(t *testing.T) {
	s, backend := wireService(t)
	backend.notReady.Store(true)
	path, ctx := serveTextTestServer(t, s)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = daemon.Relay(ctx, path, inR, outW) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "recovery-wire-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	inventory, err := session.ListTools(ctx, nil)
	if err != nil || len(inventory.Tools) != 12 {
		t.Fatal("tool discovery unavailable during recovery", err)
	}
	for _, ready := range []bool{false, true, false} {
		backend.notReady.Store(!ready)
		status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{}})
		expected := `"message_reads":false`
		if ready {
			expected = `"message_reads":true`
		}
		if err != nil || status.IsError || !strings.Contains(status.Content[0].(*mcp.TextContent).Text, expected) {
			t.Fatal("status misstated readiness", err)
		}
		fetches, acks := backend.fetches.Load(), backend.acks.Load()
		for _, call := range []struct {
			name string
			args map[string]any
		}{
			{"list_scopes", map[string]any{}},
			{"list_chats", map[string]any{}},
			{"list_messages", map[string]any{"peer": backend.peer.String()}},
			{"get_message_context", map[string]any{"message": "tgmsg:v1:chat:42:20", "before": 0, "after": 0}},
			{"search_messages", map[string]any{"peer": backend.peer.String(), "query": "q"}},
			{"list_unread", map[string]any{}},
		} {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
			if err != nil {
				t.Fatal(call.name, err)
			}
			if result.IsError == ready {
				t.Fatal(call.name + " ignored readiness")
			}
			if !ready {
				content := result.Content[0].(*mcp.TextContent).Text
				if !strings.Contains(content, "not_ready") || strings.Contains(content, "Synthetic") || result.StructuredContent != nil {
					t.Fatal("unavailable tool returned data")
				}
			}
		}
		if !ready && (backend.fetches.Load() != fetches || backend.acks.Load() != acks) {
			t.Fatal("unavailable runtime fetched or acknowledged")
		}
	}
}
