package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPollsOverStdioRelay(t *testing.T) {
	_, base := wireService(t)
	backend := newDocumentWireBackend(t, base)
	backend.candidates = backend.candidates[:1]
	c := &backend.candidates[0]
	c.Document = nil
	c.Message.Poll = &model.Poll{Question: strings.Repeat("я", 300), Options: []model.PollOption{{Text: "First"}, {Text: "Second"}}}
	service, err := reader.New(backend, base.repository, time.Now, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	scopeLease, err := base.repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := scopeLease.SaveScope(context.Background(), "", "polls", []model.PeerID{base.peer})
	if err != nil {
		t.Fatal(err)
	}
	if err := scopeLease.Close(); err != nil {
		t.Fatal(err)
	}
	path, ctx := serveTextTestServer(t, service)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = daemon.Relay(ctx, path, inR, outW) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "poll-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	inventory, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range inventory.Tools {
		data, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = resolved
	}
	if len(schemas) != 12 {
		t.Fatal("unexpected tool added")
	}
	for _, call := range []struct {
		name   string
		args   map[string]any
		search bool
	}{
		{"search_messages", map[string]any{"peer": base.peer.String(), "query": "synthetic"}, true},
		{"catch_up", map[string]any{"scope": scope.ID.String(), "since": "2026-09-05T00:00:00Z", "until": "2026-09-06T00:00:00Z"}, true},
		{"list_messages", map[string]any{"peer": base.peer.String()}, false},
		{"get_message_context", map[string]any{"message": c.Message.ID.String(), "before": 0, "after": 0}, false},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil || result.IsError {
			t.Fatal("poll call failed", call.name, err, result)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &body); err != nil {
			t.Fatal(err)
		}
		if err := schemas[call.name].Validate(body); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var mirror map[string]any
		if err := json.Unmarshal(data, &mirror); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(body, mirror) {
			t.Fatal("mirrors differ")
		}
		item := body["items"].([]any)[0].(map[string]any)
		if call.search {
			if item["has_poll"] != true || item["poll"] != nil || len([]rune(item["snippet"].(string))) != 240 || item["snippet_truncated"] != true || base.acks.Load() != 0 {
				t.Fatal("poll discovery widened delivery")
			}
		} else {
			if item["poll"].(map[string]any)["question"] != c.Message.Poll.Question {
				t.Fatal("poll body missing")
			}
		}
	}
	if base.acks.Load() != 2 || backend.downloads.Load() != 0 {
		t.Fatal("wrong side effects")
	}
	lease, err := base.repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := lease.Grant(ctx, base.peer)
	if err != nil {
		t.Fatal(err)
	}
	grant.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
	if err := lease.Save(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_messages", Arguments: map[string]any{"peer": base.peer.String()}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content[0].(*mcp.TextContent).Text, c.Message.Poll.Question) {
		t.Fatal("revoked poll leaked")
	}
}
