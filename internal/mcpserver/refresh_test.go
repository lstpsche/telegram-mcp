package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type refreshWireBackend struct {
	*wireBackend
	edited atomic.Bool
}

func (b *refreshWireBackend) History(ctx context.Context, q model.HistoryQuery) ([]model.Candidate, error) {
	rows, err := b.wireBackend.History(ctx, q)
	if err == nil && b.edited.Load() {
		rows[0].Message.Text = "Edited synthetic body"
		rows[0].Message.EditedAt = "2026-09-05T12:00:01Z"
	}
	return rows, err
}

func TestRefreshOverStdioUsesCurrentBodyAndAdvertisedSchemas(t *testing.T) {
	_, base := wireService(t)
	backend := &refreshWireBackend{wireBackend: base}
	service, err := reader.New(backend, base.repository, time.Now, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	path, ctx := serveTextTestServer(t, service)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = daemon.Relay(ctx, path, inR, outW) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "refresh-wire", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	inventory, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var schema *jsonschema.Resolved
	for _, tool := range inventory.Tools {
		if tool.Name == "get_message_context" {
			encoded, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var source jsonschema.Schema
			if err := json.Unmarshal(encoded, &source); err != nil {
				t.Fatal(err)
			}
			schema, err = source.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	call := func(args any) map[string]any {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_message_context", Arguments: args})
		if err != nil || result.IsError {
			t.Fatal("refresh wire failed", err, result)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &body); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(body); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var mirror map[string]any
		if err := json.Unmarshal(encoded, &mirror); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(body, mirror) {
			t.Fatal("refresh mirrors differ")
		}
		return body["items"].([]any)[0].(map[string]any)
	}
	id, _ := model.NewMessageID(base.peer, 20)
	first := call(map[string]any{"message": id.String()})
	token := first["observation"].(string)
	same := call(map[string]any{"refresh": []string{token}})
	if same["refresh_state"] != "unchanged" || same["text"] != first["text"] {
		t.Fatal("unchanged wire result")
	}
	backend.edited.Store(true)
	changed := call(map[string]any{"refresh": []string{token}})
	if changed["refresh_state"] != "changed" || changed["edited_at"] != "2026-09-05T12:00:01Z" || changed["text"] == first["text"] {
		t.Fatal("changed wire result")
	}
	before := base.fetches.Load()
	for _, args := range []any{map[string]any{"refresh": []string{}}, map[string]any{"refresh": []string{token}, "before": 0}, map[string]any{"refresh": []string{token}, "message": id.String()}, map[string]any{"refresh": []string{token, token}}, map[string]any{"refresh": []string{"bad-token"}}} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_message_context", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatal("invalid refresh accepted")
		}
	}
	if before != base.fetches.Load() {
		t.Fatal("invalid refresh reached provider")
	}
}
