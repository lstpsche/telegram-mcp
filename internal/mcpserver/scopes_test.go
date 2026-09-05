package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

type multiPeerWireBackend struct {
	*wireBackend
	second model.PeerID
}

func (f *multiPeerWireBackend) message(peer model.PeerID) (model.Candidate, error) {
	if peer != f.peer && peer != f.second {
		return model.Candidate{}, errors.New("unexpected synthetic peer")
	}
	id, err := model.NewMessageID(peer, 20)
	if err != nil {
		return model.Candidate{}, err
	}
	return model.Candidate{SentAt: 1788609600, Message: model.Message{ID: id, Author: f.author, Date: "2026-09-05T12:00:00Z", Text: "synthetic scoped context"}}, nil
}
func (f *multiPeerWireBackend) Search(_ context.Context, q model.SearchQuery) ([]model.Candidate, error) {
	f.fetches.Add(1)
	item, err := f.message(q.Peer)
	if err != nil {
		return nil, err
	}
	if q.Before != 0 {
		return []model.Candidate{}, nil
	}
	return []model.Candidate{item}, nil
}
func (f *multiPeerWireBackend) History(_ context.Context, q model.HistoryQuery) ([]model.Candidate, error) {
	f.fetches.Add(1)
	item, err := f.message(q.Peer)
	if err != nil {
		return nil, err
	}
	if q.Target != 20 {
		return nil, errors.New("unexpected synthetic target")
	}
	return []model.Candidate{item}, nil
}
func (f *multiPeerWireBackend) Acknowledge(_ context.Context, peer model.PeerID, through int32) error {
	if (peer != f.peer && peer != f.second) || through != 20 {
		return errors.New("unexpected synthetic receipt")
	}
	f.acks.Add(1)
	return nil
}

func TestAgentDiscoversScopeSearchesTwoPeersAndOpensExactContext(t *testing.T) {
	for _, operation := range []string{"search_messages", "catch_up"} {
		t.Run(operation, func(t *testing.T) {
			_, base := wireService(t)
			second, _ := model.NewPeerID(model.PeerKindChat, 43)
			backend := &multiPeerWireBackend{wireBackend: base, second: second}
			lease, err := base.repository.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Save(context.Background(), policy.Grant{Peer: second, Author: base.author, MinID: 10, MaxID: 30, ReadThrough: 30, Profile: policy.ProfileSelfAuthored, ExpiresAt: time.Now().Add(time.Hour), Eligible: true}); err != nil {
				t.Fatal(err)
			}
			scope, err := lease.SaveScope(context.Background(), "", "work", []model.PeerID{second, base.peer})
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			service, err := reader.New(backend, base.repository, time.Now, []byte(strings.Repeat("k", 32)))
			if err != nil {
				t.Fatal(err)
			}
			path, ctx := serveTextTestServer(t, service)
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			go func() { _ = daemon.Relay(ctx, path, inR, outW) }()
			client := mcp.NewClient(&mcp.Implementation{Name: "scoped-agent", Version: "1"}, nil)
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
				encoded, err := json.Marshal(tool.OutputSchema)
				if err != nil {
					t.Fatal(err)
				}
				var schema jsonschema.Schema
				if err := json.Unmarshal(encoded, &schema); err != nil {
					t.Fatal(err)
				}
				resolved, err := schema.Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				schemas[tool.Name] = resolved
			}
			call := func(name string, args any) map[string]any {
				t.Helper()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
				if err != nil || result.IsError {
					t.Fatalf("%s: %v %#v", name, err, result)
				}
				var page map[string]any
				if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &page); err != nil {
					t.Fatal(err)
				}
				if err := schemas[name].Validate(page); err != nil {
					t.Fatal(err)
				}
				return page
			}
			discovered := call("list_scopes", map[string]any{})["items"].([]any)[0].(map[string]any)
			if discovered["id"] != scope.ID.String() || discovered["eligible_peers"] != float64(2) || base.fetches.Load() != 0 {
				t.Fatal("discovery did not expose the configured selection")
			}

			args := map[string]any{"scope": discovered["id"], "limit": 1}
			if operation == "catch_up" {
				args["since"] = "2026-09-05T12:00:00Z"
				args["until"] = "2026-09-05T12:00:01Z"
			} else {
				args["query"] = "synthetic"
			}
			first := call(operation, args)
			args["cursor"] = first["next_cursor"]
			next := call(operation, args)
			if operation == "catch_up" {
				args["cursor"] = next["next_cursor"]
				terminal := call(operation, args)
				coverage := terminal["scope"].(map[string]any)
				if terminal["next_cursor"] != nil || coverage["completed_peers"] != float64(2) {
					t.Fatal("catch-up did not finish")
				}
				for _, peer := range coverage["catch_up"].(map[string]any)["peers"].([]any) {
					if peer.(map[string]any)["state"] != "complete" {
						t.Fatal("incomplete peer at end")
					}
				}
			}

			a := first["items"].([]any)[0].(map[string]any)["id"]
			b := next["items"].([]any)[0].(map[string]any)["id"]
			if a != "tgmsg:v1:chat:42:20" || b != "tgmsg:v1:chat:43:20" || base.acks.Load() != 0 {
				t.Fatal("scope continuation lost typed peer identity or acknowledged search")
			}
			full := call("get_message_context", map[string]any{"message": b})
			if full["items"].([]any)[0].(map[string]any)["id"] != b || full["read_effect"].(map[string]any)["through_message_id"] != b || base.acks.Load() != 1 {
				t.Fatal("exact scoped hit did not use the context receipt flow")
			}

		})
	}
}
