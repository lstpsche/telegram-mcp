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

type discussionWireBackend struct {
	*wireBackend
	source, group model.PeerID
	root          model.MessageID
}

func (b *discussionWireBackend) History(_ context.Context, q model.HistoryQuery) ([]model.Candidate, error) {
	b.fetches.Add(1)
	id, _ := model.NewMessageID(q.Peer, 20)
	m := model.Message{ID: id, Author: b.author, Date: "2026-09-05T12:00:00Z", Text: "synthetic reply"}
	if q.Peer == b.source {
		m.Author = b.source
		m.ChannelPost = &model.ChannelPost{}
		m.DiscussionPeer = b.group.String()
	} else {
		m.ReplyTo = &b.root
		m.ThreadRoot = &b.root
	}
	return []model.Candidate{{Message: m}}, nil
}
func (b *discussionWireBackend) Search(ctx context.Context, q model.SearchQuery) ([]model.Candidate, error) {
	return b.History(ctx, model.HistoryQuery{Peer: q.Peer})
}
func (b *discussionWireBackend) Discussion(_ context.Context, _ model.MessageID, _ model.PeerID) (model.MessageID, error) {
	return b.root, nil
}

func TestDiscussionExplorationOverStdio(t *testing.T) {
	_, base := wireService(t)
	source, _ := model.NewPeerID(model.PeerKindChannel, 42)
	group, _ := model.NewPeerID(model.PeerKindChannel, 99)
	root, _ := model.NewMessageID(group, 10)
	base.peer = source
	lease, err := base.repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.SetFullRead(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	backend := &discussionWireBackend{wireBackend: base, source: source, group: group, root: root}
	service, err := reader.New(backend, base.repository, time.Now, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	path, ctx := serveTextTestServer(t, service)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = daemon.Relay(ctx, path, inR, outW) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "discussion-wire", Version: "1"}, nil)
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
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
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
			t.Fatal("wire failure", err, result)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &value); err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(value); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value, result.StructuredContent) {
			t.Fatal("mirrors differ")
		}
		return value
	}
	post, _ := model.NewMessageID(source, 20)
	contextResult := call("get_message_context", map[string]any{"message": post.String(), "resolve_discussion": true})
	item := contextResult["items"].([]any)[0].(map[string]any)
	if item["discussion_root"] != root.String() || item["discussion_peer"] != group.String() || base.acks.Load() != 1 {
		t.Fatal("discussion context contract")
	}
	searchResult := call("search_messages", map[string]any{"peer": group.String(), "thread_root": root.String(), "reply_to": root.String()})
	hit := searchResult["items"].([]any)[0].(map[string]any)
	if hit["thread_root"] != root.String() || base.acks.Load() != 1 {
		t.Fatal("reply discovery contract")
	}
	before := base.fetches.Load()
	for _, args := range []map[string]any{
		{"peer": source.String(), "reply_to": root.String()},
		{"scope": "tgscope:v1:0123456789abcdef0123456789abcdef", "thread_root": root.String()},
		{"peer": group.String(), "reply_to": "bad"},
		{"peer": group.String(), "thread_root": nil},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatal("invalid reply selector accepted", err)
		}
	}
	if base.fetches.Load() != before || base.acks.Load() != 1 {
		t.Fatal("invalid input caused I/O")
	}
	groupMessage, _ := model.NewMessageID(group, 20)
	batch := call("get_message_context", map[string]any{"messages": []string{post.URL(), groupMessage.String()}})
	if len(batch["items"].([]any)) != 2 || len(batch["contexts"].([]any)) != 2 || len(batch["read_effect"].(map[string]any)["through_message_ids"].([]any)) != 2 || base.acks.Load() != 3 {
		t.Fatal("batch wire contract")
	}
	for _, value := range batch["items"].([]any) {
		item := value.(map[string]any)
		if item["url"] == "" {
			t.Fatal("missing citation link")
		}
	}
	public := call("get_message_context", map[string]any{"message": "https://t.me/synthetic_publisher/20"})
	if public["items"].([]any)[0].(map[string]any)["id"] != post.String() || base.acks.Load() != 4 {
		t.Fatal("public link contract")
	}
	before = base.fetches.Load()
	for _, args := range []map[string]any{
		{"messages": []string{post.String(), post.URL()}},
		{"message": post.String(), "messages": []string{groupMessage.String()}},
		{"messages": []string{}},
		{"messages": []string{post.String(), "https://evil.test/c/42/20"}},
		{"messages": []string{post.String(), groupMessage.String()}, "before": 49, "after": 49},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_message_context", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatal("invalid batch accepted", err)
		}
	}
	if base.fetches.Load() != before || base.acks.Load() != 4 {
		t.Fatal("invalid batch performed I/O")
	}
}

func (b *discussionWireBackend) ResolveMessagePublisher(_ context.Context, username string) (model.Chat, error) {
	if username != "synthetic_publisher" {
		return model.Chat{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	return model.Chat{ID: b.source, Broadcast: true}, nil
}

func (b *discussionWireBackend) Acknowledge(_ context.Context, peer model.PeerID, through int32) error {
	if (peer != b.source && peer != b.group) || through != 20 {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	b.acks.Add(1)
	return nil
}
