package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fullWireBackend struct{ *imageWireBackend }

func (f *fullWireBackend) Dialogs(_ context.Context, position model.DialogPosition, limit int) (model.DialogPage, error) {
	f.fetches.Add(1)
	if position.Folder == 1 {
		return model.DialogPage{Items: []model.DialogEntry{}}, nil
	}
	return model.DialogPage{Items: []model.DialogEntry{{Chat: model.Chat{ID: f.peer, Title: "Synthetic supergroup"}, Unread: model.Unread{Peer: f.peer, Count: 2}}}, Scanned: 1, Next: &model.DialogPosition{Folder: 1}}, nil
}

func TestFullReadWorkflowAndRevocationOverStdio(t *testing.T) {
	_, base := wireService(t)
	ctx := context.Background()
	lease, err := base.repository.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Revoke(ctx, base.peer); err != nil {
		t.Fatal(err)
	}
	base.peer, err = model.NewPeerID(model.PeerKindChannel, 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	scope, err := lease.SaveScope(ctx, "", "work", []model.PeerID{base.peer})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	backend := &fullWireBackend{newImageWireBackend(t, base)}
	backend.candidates[1].Message.Author, _ = model.NewPeerID(model.PeerKindUser, 8)
	service, err := reader.New(backend, base.repository, time.Now, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	path, ctx := serveTextTestServer(t, service)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = daemon.Relay(ctx, path, inR, outW) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "full-read-test", Version: "1"}, nil)
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
		schemas[tool.Name], err = schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(schemas) != 12 {
		t.Fatal("unexpected control tools")
	}
	call := func(name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %v %#v", name, err, result)
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &content); err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(content); err != nil {
			t.Fatal(name, err)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		mirror, err := json.Marshal(content)
		if err != nil || !bytes.Equal(encoded, mirror) {
			t.Fatal("metadata mirrors differ", err)
		}
		return result, content
	}
	_, scopes := call("list_scopes", map[string]any{})
	if scopes["items"].([]any)[0].(map[string]any)["eligible_peers"] != float64(1) {
		t.Fatal("full scope excluded")
	}
	_, chats := call("list_chats", map[string]any{"limit": 1})
	cursor := chats["next_cursor"].(string)
	_, last := call("list_chats", map[string]any{"limit": 1, "cursor": cursor})
	if len(last["items"].([]any)) != 0 || last["next_cursor"] != nil {
		t.Fatal("pagination did not terminate")
	}
	_, unread := call("list_unread", map[string]any{})
	if len(unread["items"].([]any)) != 1 || backend.acks.Load() != 0 {
		t.Fatal("unread workflow changed receipts")
	}
	_, search := call("search_messages", map[string]any{"scope": scope.ID.String(), "query": "synthetic"})
	if len(search["items"].([]any)) != 1 || backend.acks.Load() != 0 {
		t.Fatal("search failed or acknowledged")
	}
	_, history := call("list_messages", map[string]any{"peer": base.peer.String()})
	items := history["items"].([]any)
	if len(items) != 2 || items[1].(map[string]any)["author"] != backend.candidates[1].Message.Author.String() || backend.acks.Load() != 1 {
		t.Fatal("multiple authors not delivered after receipt")
	}
	handle := items[1].(map[string]any)["image"].(map[string]any)["handle"]
	result, _ := call("open_image", map[string]any{"handle": handle})
	if len(result.Content) != 2 || !bytes.Equal(result.Content[1].(*mcp.ImageContent).Data, backend.data[backend.candidates[1].Message.ID]) || backend.acks.Load() != 2 {
		t.Fatal("image bytes or receipt differ")
	}
	call("get_message_context", map[string]any{"message": backend.candidates[0].Message.ID.String(), "before": 0, "after": 0})
	lease, err = base.repository.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.SetFullRead(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	fetches, acks, downloads := backend.fetches.Load(), backend.acks.Load(), backend.downloads.Load()
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"list_messages", map[string]any{"peer": base.peer.String()}},
		{"open_image", map[string]any{"handle": handle}},
		{"list_chats", map[string]any{"limit": 1, "cursor": cursor}},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil || !result.IsError || result.StructuredContent != nil || len(result.Content) != 1 {
			t.Fatal("revocation released content", test.name, err)
		}
	}
	if backend.fetches.Load() != fetches || backend.acks.Load() != acks || backend.downloads.Load() != downloads {
		t.Fatal("revoked request reached Telegram")
	}
}
