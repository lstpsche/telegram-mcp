package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSearchUnreadAndContextOverStdio(t *testing.T) {
	service, backend := wireService(t)
	lease, err := backend.repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	denied, _ := model.NewPeerID(model.PeerKindChat, 999)
	scope, err := lease.SaveScope(context.Background(), "", "work", []model.PeerID{backend.peer, denied})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := lease.SaveScope(context.Background(), "", "empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	path, ctx := serveTextTestServer(t, service)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = daemon.Relay(ctx, path, inR, outW) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "search-wire", Version: "1"}, nil)
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
		if (tool.Name == "search_messages" || tool.Name == "list_unread") && !tool.Annotations.ReadOnlyHint {
			t.Fatal("metadata tool has incorrect side effects")
		}
	}
	if len(schemas) != 10 || schemas["search_messages"] == nil || schemas["list_unread"] == nil {
		t.Fatal("missing tools")
	}
	call := func(name string, args any) map[string]any {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %v %#v", name, err, result)
		}
		raw := result.Content[0].(*mcp.TextContent).Text
		var content map[string]any
		if err := json.Unmarshal([]byte(raw), &content); err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(content); err != nil {
			t.Fatalf("output schema mismatch %s: %v", name, err)
		}
		expected, _ := json.Marshal(content)
		actual, _ := json.Marshal(result.StructuredContent)
		if string(expected) != string(actual) {
			t.Fatal("wire mirrors differ")
		}
		return content
	}
	beforeScopes := backend.fetches.Load()
	scopes := call("list_scopes", map[string]any{})
	if backend.fetches.Load() != beforeScopes || len(scopes["items"].([]any)) != 2 || scopes["freshness"].(map[string]any)["telegram"] != "unavailable" {
		t.Fatal("scope discovery fetched or lied about freshness")
	}
	for _, value := range scopes["items"].([]any) {
		item := value.(map[string]any)
		if item["id"] == scope.ID.String() && (item["eligible_peers"] != float64(1) || item["excluded_peers"] != float64(1)) {
			t.Fatal("scope eligibility mismatch")
		}
	}
	for _, name := range []string{"list_chats", "list_unread", "search_messages"} {
		args := map[string]any{"scope": scope.ID.String()}
		if name == "search_messages" {
			args["query"] = "synthetic"
			args["limit"] = 2
		}
		scoped := call(name, args)
		coverage := scoped["scope"].(map[string]any)
		if scoped["partial"] != true || coverage["eligible_peers"] != float64(1) || coverage["excluded_peers"] != float64(1) || coverage["queried_peers"] != float64(1) || backend.acks.Load() != 0 {
			t.Fatal("scoped result lost coverage or changed read state")
		}
		if name == "search_messages" {
			args["cursor"] = scoped["next_cursor"]
			last := call(name, args)
			if last["next_cursor"] != nil || len(last["items"].([]any)) != 1 || last["scope"].(map[string]any)["completed_peers"] != float64(1) {
				t.Fatal("scoped continuation mismatch")
			}
		}
		emptyArgs := map[string]any{"scope": empty.ID.String()}
		if name == "search_messages" {
			emptyArgs["query"] = "synthetic"
		}
		before := backend.fetches.Load()
		page := call(name, emptyArgs)
		if len(page["items"].([]any)) != 0 || backend.fetches.Load() != before || page["partial"] != false {
			t.Fatal("empty scope widened to all grants")
		}
	}
	first := call("search_messages", map[string]any{"peer": backend.peer.String(), "query": "synthetic", "limit": 2})
	if first["read_effect"].(map[string]any)["kind"] != "none" || backend.acks.Load() != 0 {
		t.Fatal("search acknowledged history")
	}
	token, ok := first["next_cursor"].(string)
	if !ok {
		t.Fatal("missing continuation")
	}
	second := call("search_messages", map[string]any{"peer": backend.peer.String(), "query": "synthetic", "limit": 2, "cursor": token})
	if second["next_cursor"] != nil || len(second["items"].([]any)) != 1 {
		t.Fatal("pagination failed")
	}
	unread := call("list_unread", map[string]any{})
	if unread["read_effect"].(map[string]any)["kind"] != "none" || backend.acks.Load() != 0 {
		t.Fatal("unread acknowledged history")
	}
	id := first["items"].([]any)[0].(map[string]any)["id"]
	full := call("get_message_context", map[string]any{"message": id})
	if full["read_effect"].(map[string]any)["kind"] != "history_marked_read" || backend.acks.Load() != 1 {
		t.Fatal("search hit did not enter full context receipt flow")
	}
	call("search_messages", map[string]any{"peer": backend.peer.String(), "query": "  " + strings.Repeat("q", 256) + "  ", "limit": 2})
	before := backend.fetches.Load()
	for _, args := range []string{
		`{"query":"q"}`,
		`{"peer":"tgpeer:v1:chat:42","scope":"` + scope.ID.String() + `","query":"q"}`,
		`{"scope":null,"query":"q"}`,
		`{"scope":"work","query":"q"}`,
		`{"scope":"` + scope.ID.String() + `","scope":"` + scope.ID.String() + `","query":"q"}`,
		`{"scope":"` + scope.ID.String() + `","peer":null,"query":"q"}`,
		`{"peer":"tgpeer:v1:chat:42","query":null}`,
		`{"peer":"tgpeer:v1:chat:42","query":" "}`,
		`{"peer":"tgpeer:v1:chat:42","Query":"synthetic"}`,
		`{"peer":"tgpeer:v1:chat:42","query":"a","query":"b"}`,
		`{"peer":"tgpeer:v1:chat:42","query":"q","cursor":null}`,
		`{"peer":"tgpeer:v1:chat:42","query":"q","cursor":""}`,
		`{"peer":"tgpeer:v1:chat:42","query":"q","limit":null}`,
		`{"peer":"tgpeer:v1:chat:42","query":"q","limit":101}`,
		`{"peer":"tgpeer:v1:chat:42","query":"q","private-marker":"sensitive"}`,
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: json.RawMessage(args)})
		if err != nil || !result.IsError {
			t.Fatal("invalid search accepted")
		}
		raw := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(raw, "invalid_input") || strings.Contains(raw, "sensitive") {
			t.Fatal("unsafe input error")
		}
	}
	for _, name := range []string{"list_scopes", "list_chats", "list_unread"} {
		for _, args := range []string{`{"scope":null}`, `{"Scope":"` + scope.ID.String() + `"}`, `{"scope":"` + scope.ID.String() + `","scope":"` + scope.ID.String() + `"}`} {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(args)})
			if err != nil || !result.IsError {
				t.Fatal("invalid scope selector accepted")
			}
		}
	}
	for _, args := range []map[string]any{{"peer": backend.peer.String(), "query": "changed", "limit": 2, "cursor": token}, {"peer": "tgpeer:v1:chat:999", "query": "q"}} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatal("denied search accepted")
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_unread", Arguments: map[string]any{"peer": backend.peer.String()}})
	if err != nil || !result.IsError || backend.fetches.Load() != before || backend.acks.Load() != 1 {
		t.Fatal("invalid input reached Telegram")
	}
}

func TestUnconfiguredSearchToolsRemainUnavailable(t *testing.T) {
	for _, name := range []string{"search_messages", "list_unread", "list_scopes"} {
		args := json.RawMessage(`{}`)
		if name == "search_messages" {
			args = json.RawMessage(`{"peer":"tgpeer:v1:chat:42","query":"q"}`)
		}
		if _, err := callText(context.Background(), nil, "req_unavailable", name, args); err == nil {
			t.Fatal("unconfigured tool available")
		}
	}
}
