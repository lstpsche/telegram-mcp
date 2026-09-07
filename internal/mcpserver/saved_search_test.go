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
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSavedOrganizationOverStdio(t *testing.T) {
	_, base := wireService(t)
	base.peer, _ = model.NewPeerID(model.PeerKindSelf, base.author.TelegramID())
	lease, err := base.repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = lease.Save(context.Background(), policy.Grant{Peer: base.peer, Author: base.author, Profile: policy.ProfileConsented, MinID: 10, MaxID: 30, ReadThrough: 30, ExpiresAt: time.Now().Add(time.Hour), Eligible: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	backend := newDocumentWireBackend(t, base)
	backend.candidates = backend.candidates[:1]
	c := &backend.candidates[0]
	c.Document = nil
	c.Message.Text = "synthetic saved copy"
	c.Message.SavedPeer = "tgpeer:v1:chat:99"
	c.Message.Reactions = &model.Reactions{AsTags: true, Counts: []model.ReactionCount{{Kind: "emoji", Emoji: "📌", Count: 1}, {Kind: "custom_emoji", CustomEmojiID: "123", Count: 1}}}
	service, err := reader.New(backend, base.repository, time.Now, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	path, ctx := serveTextTestServer(t, service)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = daemon.Relay(ctx, path, inR, outW) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "saved-wire", Version: "1"}, nil)
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
		raw, _ := json.Marshal(tool.OutputSchema)
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = resolved
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatal("wire operation failed", err, result)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &value); err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(value); err != nil {
			t.Fatal("schema mismatch", err)
		}
		if !reflect.DeepEqual(value, result.StructuredContent) {
			t.Fatal("wire mirrors differ")
		}
		return value
	}
	for _, tag := range []map[string]any{{"kind": "emoji", "emoji": "📌"}, {"kind": "custom_emoji", "custom_emoji_id": "123"}} {
		result := call("search_messages", map[string]any{"peer": base.peer.String(), "query": "synthetic", "saved_peer": c.Message.SavedPeer, "saved_tag": tag, "sender": base.author.String(), "since": "2026-09-05T12:00:00Z", "until": "2026-09-05T12:00:01Z"})
		items := result["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["saved_peer"] != c.Message.SavedPeer || base.acks.Load() != 0 {
			t.Fatal("saved discovery failed")
		}
	}
	result := call("list_messages", map[string]any{"peer": base.peer.String()})
	if result["items"].([]any)[0].(map[string]any)["saved_peer"] != c.Message.SavedPeer || base.acks.Load() != 1 {
		t.Fatal("saved history contract")
	}
	before := base.fetches.Load()
	for _, extra := range []map[string]any{
		{"saved_tag": nil}, {"saved_tag": map[string]any{}}, {"saved_tag": map[string]any{"kind": "paid"}}, {"saved_tag": map[string]any{"kind": "emoji", "emoji": "📌", "custom_emoji_id": "1"}},
		{"saved_tag": map[string]any{"kind": "custom_emoji", "custom_emoji_id": "0"}}, {"saved_tag": map[string]any{"kind": "emoji", "emoji": "📌", "count": 1}},
		{"saved_peer": nil}, {"saved_peer": "tgpeer:v1:channel:99:topic:1"}, {"peer": "tgpeer:v1:chat:42", "saved_peer": "tgpeer:v1:chat:99"},
		{"scope": "tgscope:v1:0123456789abcdef0123456789abcdef", "saved_tag": map[string]any{"kind": "emoji", "emoji": "📌"}},
	} {
		args := map[string]any{"peer": base.peer.String(), "query": "synthetic"}
		for k, v := range extra {
			args[k] = v
		}
		if _, ok := extra["scope"]; ok {
			delete(args, "peer")
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatal("invalid saved filter accepted", err)
		}
	}
	if base.fetches.Load() != before || base.acks.Load() != 1 {
		t.Fatal("invalid saved filter caused I/O")
	}
}
