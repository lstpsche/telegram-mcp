package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
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

type mediaSearchWireBackend struct {
	*documentWireBackend
	kind model.SearchMediaType
}

func (f *mediaSearchWireBackend) Search(_ context.Context, q model.SearchQuery) ([]model.Candidate, error) {
	f.fetches.Add(1)
	if q.Peer != f.peer || q.MediaType != f.kind || !q.PinnedOnly || (q.Query != "" && q.Query != "caption") || q.Window != nil {
		return nil, errors.New("unexpected media search")
	}
	return f.candidates, nil
}

func TestMediaSearchOverStdioRelay(t *testing.T) {
	for _, spec := range []struct {
		kind   model.SearchMediaType
		field  string
		source model.MediaSource
	}{
		{model.SearchMediaPhoto, "image", model.MediaSource{Kind: "photo", MIMEType: "image/jpeg", Width: 32, Height: 32, Size: 100}},
		{model.SearchMediaImageFile, "image", model.MediaSource{Kind: "document", MIMEType: "image/png", Width: 32, Height: 32, Size: 100}},
		{model.SearchMediaPDF, "document", model.MediaSource{Kind: "document", MIMEType: "application/pdf", Size: 100}},
		{model.SearchMediaTextFile, "document", model.MediaSource{Kind: "document", MIMEType: "text/plain", Size: 100}},
		{model.SearchMediaVoiceNote, "voice_note", model.MediaSource{Kind: "voice", MIMEType: "audio/ogg", Size: 100, Duration: 1}},
	} {
		t.Run(string(spec.kind), func(t *testing.T) {
			_, base := wireService(t)
			backend := &mediaSearchWireBackend{documentWireBackend: newDocumentWireBackend(t, base), kind: spec.kind}
			c := backend.candidates[0]
			c.Document = nil
			c.Message.Pinned = true
			spec.source.Fingerprint = strings.Repeat("a", 64)
			switch spec.field {
			case "image":
				c.Image = &spec.source
			case "document":
				c.Document = &spec.source
			case "voice_note":
				c.Voice = &spec.source
			}
			backend.candidates = []model.Candidate{c}
			lease, err := base.repository.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			grant, err := lease.Grant(context.Background(), base.peer)
			if err != nil {
				t.Fatal(err)
			}
			grant.Images, grant.Documents, grant.VoiceNotes = true, true, true
			if err := lease.Save(context.Background(), grant); err != nil {
				t.Fatal(err)
			}
			scope, err := lease.SaveScope(context.Background(), "", "media", []model.PeerID{base.peer})
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
			client := mcp.NewClient(&mcp.Implementation{Name: "media-search-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			inventory, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(inventory.Tools) != 12 {
				t.Fatal("unexpected tool inventory")
			}
			var output *jsonschema.Resolved
			for _, tool := range inventory.Tools {
				if tool.Name != "search_messages" {
					continue
				}
				data, err := json.Marshal(tool.OutputSchema)
				if err != nil {
					t.Fatal(err)
				}
				var schema jsonschema.Schema
				if err := json.Unmarshal(data, &schema); err != nil {
					t.Fatal(err)
				}
				output, err = schema.Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				if !tool.Annotations.ReadOnlyHint {
					t.Fatal("search lost read-only hint")
				}
			}
			if output == nil {
				t.Fatal("search tool missing")
			}
			for _, selector := range []map[string]any{{"peer": base.peer.String()}, {"scope": scope.ID.String()}} {
				for _, query := range []string{"", "caption"} {
					selector["media_type"], selector["pinned_only"] = spec.kind, true
					if query != "" {
						selector["query"] = query
					}
					result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: selector})
					if err != nil || result.IsError {
						t.Fatal("media search failed", err, result)
					}
					var body, mirror map[string]any
					if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &body); err != nil {
						t.Fatal(err)
					}
					data, err := json.Marshal(result.StructuredContent)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(data, &mirror); err != nil {
						t.Fatal(err)
					}
					if err := output.Validate(body); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(body, mirror) {
						t.Fatal("media result mirrors differ")
					}
					items := body["items"].([]any)
					if len(items) != 1 {
						t.Fatal("attachment missing")
					}
					item := items[0].(map[string]any)
					descriptor, ok := item[spec.field].(map[string]any)
					if !ok || descriptor["mime_type"] != spec.source.MIMEType || descriptor["handle"] == "" || item["snippet"] != "" || item["pinned"] != true || body["read_effect"].(map[string]any)["kind"] != "none" {
						t.Fatal("wrong media discovery output")
					}
				}
			}
			for _, value := range []any{"", "video", "PDF", nil, 1, []string{"pdf"}} {
				calls := base.fetches.Load()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: map[string]any{"peer": base.peer.String(), "media_type": value, "query": "caption"}})
				if err == nil && !result.IsError {
					t.Fatal("invalid media selector accepted")
				}
				if base.fetches.Load() != calls {
					t.Fatal("invalid media selector reached backend")
				}
			}
			if base.acks.Load() != 0 || backend.downloads.Load() != 0 {
				t.Fatal("media discovery caused side effects")
			}
		})
	}
}
