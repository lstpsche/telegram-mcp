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

func TestTextMediaOverStdioRelay(t *testing.T) {
	for _, variant := range []string{"poll", "available", "pending", "unavailable", "standalone", "empty_reactions"} {
		t.Run(variant, func(t *testing.T) {
			kind := "link_preview"
			if variant == "poll" {
				kind = "poll"
			}
			_, base := wireService(t)
			backend := newDocumentWireBackend(t, base)
			backend.candidates = backend.candidates[:1]
			c := &backend.candidates[0]
			c.Document = nil
			c.Message.Pinned = true
			c.UnreadMention = true
			c.Message.Reactions = &model.Reactions{Minimal: true, Counts: []model.ReactionCount{{Kind: "emoji", Emoji: "👍", Count: 3}, {Kind: "custom_emoji", CustomEmojiID: "9223372036854775807", Count: 2}, {Kind: "paid", Count: 9}}}
			if variant == "empty_reactions" {
				c.Message.Reactions.Counts = []model.ReactionCount{}
			}
			var expected any
			if kind == "poll" {
				c.Message.Poll = &model.Poll{Question: strings.Repeat("я", 300), Options: []model.PollOption{{Text: "First"}, {Text: "Second"}}}
				expected = c.Message.Poll
			} else {
				c.Message.Text = strings.Repeat("я", 300)
				c.Message.LinkPreview = &model.LinkPreview{State: "available", URL: "https://example.invalid", Title: "Preview title", Manual: true}
				if variant == "pending" || variant == "unavailable" {
					c.Message.LinkPreview = &model.LinkPreview{State: variant}
				}
				if variant == "standalone" {
					c.Message.Text = ""
				}
				expected = c.Message.LinkPreview
			}
			service, err := reader.New(backend, base.repository, time.Now, []byte(strings.Repeat("k", 32)))
			if err != nil {
				t.Fatal(err)
			}
			scopeLease, err := base.repository.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			scope, err := scopeLease.SaveScope(context.Background(), "", "text-media", []model.PeerID{base.peer})
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
			client := mcp.NewClient(&mcp.Implementation{Name: "text-media-test", Version: "1"}, nil)
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
				{"search_messages", map[string]any{"peer": base.peer.String(), "pinned_only": true}, true},
				{"search_messages", map[string]any{"scope": scope.ID.String(), "pinned_only": true}, true},
				{"search_messages", map[string]any{"peer": base.peer.String(), "unread_mentions_only": true}, true},
				{"search_messages", map[string]any{"scope": scope.ID.String(), "unread_mentions_only": true}, true},
				{"catch_up", map[string]any{"scope": scope.ID.String(), "since": "2026-09-05T00:00:00Z", "until": "2026-09-06T00:00:00Z"}, true},
				{"list_messages", map[string]any{"peer": base.peer.String()}, false},
				{"get_message_context", map[string]any{"message": c.Message.ID.String(), "before": 0, "after": 0}, false},
			} {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
				if err != nil || result.IsError {
					t.Fatal("text media call failed", call.name, err, result)
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
				if item["pinned"] != true {
					t.Fatal("pin state lost")
				}
				reactionJSON, err := json.Marshal(c.Message.Reactions)
				if err != nil {
					t.Fatal(err)
				}
				var expectedReactions map[string]any
				if err := json.Unmarshal(reactionJSON, &expectedReactions); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(item["reactions"], expectedReactions) {
					t.Fatal("reaction summary differs")
				}
				if call.search {
					snippet := c.Message.Text
					if c.Message.Poll != nil {
						snippet = c.Message.Poll.Question
					}
					runes := []rune(snippet)
					truncated := len(runes) > 240
					if truncated {
						runes = runes[:240]
					}
					if item["has_"+kind] != true || item[kind] != nil || item["snippet"] != string(runes) || item["snippet_truncated"] != truncated || base.acks.Load() != 0 {
						t.Fatal("discovery widened delivery")
					}
				} else {
					expectedJSON, err := json.Marshal(expected)
					if err != nil {
						t.Fatal(err)
					}
					var expectedBody map[string]any
					if err := json.Unmarshal(expectedJSON, &expectedBody); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(item[kind], expectedBody) || item["text"] != c.Message.Text {
						t.Fatal("text media body differs")
					}
				}
			}
			for _, args := range []map[string]any{
				{"peer": base.peer.String()},
				{"peer": base.peer.String(), "query": ""},
				{"peer": base.peer.String(), "unread_mentions_only": false},
				{"peer": base.peer.String(), "unread_mentions_only": "true"},
				{"peer": base.peer.String(), "pinned_only": false},
				{"peer": base.peer.String(), "pinned_only": "true"},
				{"peer": base.peer.String(), "pinned_only": true, "query": nil},
				{"peer": base.peer.String(), "scope": scope.ID.String(), "pinned_only": true},
				{"pinned_only": true},
			} {
				before := base.fetches.Load()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: args})
				if err == nil && !result.IsError {
					t.Fatal("invalid search accepted", args)
				}
				if base.fetches.Load() != before {
					t.Fatal("invalid input reached backend")
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
			if strings.Contains(result.Content[0].(*mcp.TextContent).Text, "\""+kind+"\"") || strings.Contains(result.Content[0].(*mcp.TextContent).Text, "\"reactions\"") {
				t.Fatal("revoked text media leaked")
			}
		})
	}
}
