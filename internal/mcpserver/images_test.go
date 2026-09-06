package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
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

type imageWireBackend struct {
	*wireBackend
	candidates []model.Candidate
	data       map[model.MessageID][]byte
	downloads  atomic.Int32
}

func newImageWireBackend(t *testing.T, base *wireBackend) *imageWireBackend {
	t.Helper()
	backend := &imageWireBackend{wireBackend: base, data: make(map[model.MessageID][]byte)}
	for _, spec := range []struct {
		id            int32
		kind, mime    string
		width, height int
		caption       string
	}{
		{20, "photo", "image/jpeg", 3, 2, "synthetic photo"},
		{18, "document", "image/png", 192, 192, ""},
	} {
		pixels := image.NewNRGBA(image.Rect(0, 0, spec.width, spec.height))
		seed := uint32(1)
		for y := range spec.height {
			for x := range spec.width {
				seed = seed*1664525 + 1013904223
				pixels.SetNRGBA(x, y, color.NRGBA{R: byte(seed >> 24), G: byte(seed >> 16), B: byte(seed >> 8), A: 255})
			}
		}
		var encoded bytes.Buffer
		var err error
		if spec.mime == "image/jpeg" {
			err = jpeg.Encode(&encoded, pixels, nil)
		} else {
			err = png.Encode(&encoded, pixels)
		}
		if err != nil {
			t.Fatal(err)
		}
		id, err := model.NewMessageID(base.peer, spec.id)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded.Bytes())
		backend.candidates = append(backend.candidates, model.Candidate{
			SentAt:  1788609600,
			Message: model.Message{ID: id, Author: base.author, Date: "2026-09-05T12:00:00Z", Text: spec.caption},
			Image:   &model.MediaSource{Kind: spec.kind, MIMEType: spec.mime, Width: spec.width, Height: spec.height, Size: int64(encoded.Len()), Fingerprint: hex.EncodeToString(digest[:])},
		})
		backend.data[id] = encoded.Bytes()
	}
	return backend
}

func (f *imageWireBackend) History(_ context.Context, query model.HistoryQuery) ([]model.Candidate, error) {
	f.fetches.Add(1)
	if query.Peer != f.peer {
		return nil, errors.New("unexpected image peer")
	}
	var candidates []model.Candidate
	for _, candidate := range f.candidates {
		if query.Target == 0 || candidate.Message.ID.TelegramID() == query.Target {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func (f *imageWireBackend) Search(_ context.Context, query model.SearchQuery) ([]model.Candidate, error) {
	f.fetches.Add(1)
	if query.Peer != f.peer || (query.Window == nil && query.Query != "synthetic") {
		return nil, errors.New("unexpected image search")
	}
	if query.Window != nil {
		if query.Query != "" || !query.Window.Contains(f.candidates[0].Message.Date) {
			return nil, errors.New("unexpected image date window")
		}
		return f.candidates, nil
	}
	return []model.Candidate{f.candidates[0]}, nil
}

func (f *imageWireBackend) DownloadImage(_ context.Context, expected model.Candidate) ([]byte, error) {
	f.downloads.Add(1)
	for _, candidate := range f.candidates {
		if candidate.Message.ID == expected.Message.ID && candidate.Message.Author == expected.Message.Author && expected.Image != nil && *candidate.Image == *expected.Image {
			return bytes.Clone(f.data[candidate.Message.ID]), nil
		}
	}
	return nil, errors.New("unexpected image source")
}

func (f *imageWireBackend) Acknowledge(_ context.Context, peer model.PeerID, through int32) error {
	if peer != f.peer || (through != 20 && through != 18) {
		return errors.New("unexpected image receipt")
	}
	f.acks.Add(1)
	return nil
}

func TestNativeImagesOverStdioRelay(t *testing.T) {
	_, base := wireService(t)
	backend := newImageWireBackend(t, base)
	lease, err := base.repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := lease.Grant(context.Background(), base.peer)
	if err != nil {
		t.Fatal(err)
	}
	grant.Images = true
	if err := lease.Save(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	scope, err := lease.SaveScope(context.Background(), "", "images", []model.PeerID{base.peer})
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
	client := mcp.NewClient(&mcp.Implementation{Name: "image-wire-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	inventory, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := make(map[string]*jsonschema.Resolved)
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
		if tool.Name == "open_image" && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint) {
			t.Fatal("image tool misrepresents its read effects")
		}
	}
	if len(schemas) != 12 || schemas["open_image"] == nil {
		t.Fatal("missing native image tool")
	}
	call := func(name string, args any) (*mcp.CallToolResult, map[string]any) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %v %#v", name, err, result)
		}
		if len(result.Content) == 0 {
			t.Fatal("missing metadata mirror")
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatal("first result block is not metadata")
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(text.Text), &content); err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(content); err != nil {
			t.Fatalf("%s output violates schema: %v", name, err)
		}
		expected, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := json.Marshal(result.StructuredContent)
		if err != nil || !bytes.Equal(expected, actual) {
			t.Fatal("structured and text metadata differ", err)
		}
		if content["untrusted_content"] != true {
			t.Fatal("missing untrusted-content annotation")
		}
		for _, candidate := range backend.candidates {
			if strings.Contains(text.Text, candidate.Image.Fingerprint) {
				t.Fatal("adapter fingerprint leaked into metadata")
			}
		}
		if name != "open_image" && len(result.Content) != 1 {
			t.Fatal("discovery returned native media")
		}
		return result, content
	}
	_, scopes := call("list_scopes", map[string]any{})
	discovered := scopes["items"].([]any)[0].(map[string]any)
	if discovered["id"] != scope.ID.String() || backend.fetches.Load() != 0 {
		t.Fatal("scope discovery changed its selection or fetched Telegram")
	}
	_, search := call("search_messages", map[string]any{"scope": discovered["id"], "query": "synthetic"})
	photo := search["items"].([]any)[0].(map[string]any)
	if search["read_effect"].(map[string]any)["kind"] != "none" || backend.acks.Load() != 0 || backend.downloads.Load() != 0 || photo["snippet"] != "synthetic photo" {
		t.Fatal("image search changed read state, downloaded media, or lost caption")
	}
	_, catchUp := call("catch_up", map[string]any{"scope": discovered["id"], "since": "2026-09-05T00:00:00Z", "until": "2026-09-06T00:00:00Z"})
	caught := catchUp["items"].([]any)
	if len(caught) != len(backend.candidates) || backend.acks.Load() != 0 || backend.downloads.Load() != 0 {
		t.Fatal("catch-up image discovery changed state or lost images")
	}
	photo = caught[0].(map[string]any)
	open := func(item map[string]any, expected model.Candidate) {
		t.Helper()
		descriptor := item["image"].(map[string]any)
		beforeAcks, beforeDownloads := backend.acks.Load(), backend.downloads.Load()
		result, content := call("open_image", map[string]any{"handle": descriptor["handle"]})
		if len(result.Content) != 2 {
			t.Fatal("expected exactly one metadata block and one native image")
		}
		native, ok := result.Content[1].(*mcp.ImageContent)
		if !ok || native.MIMEType != expected.Image.MIMEType || !bytes.Equal(native.Data, backend.data[expected.Message.ID]) {
			t.Fatal("native image did not preserve its exact bytes and MIME")
		}
		decoded, _, err := image.DecodeConfig(bytes.NewReader(native.Data))
		if err != nil || decoded.Width != expected.Image.Width || decoded.Height != expected.Image.Height {
			t.Fatal("native image dimensions differ from source metadata", err)
		}
		opened := content["items"].([]any)[0].(map[string]any)
		if opened["id"] != expected.Message.ID.String() || opened["author"] != expected.Message.Author.String() || opened["date"] != expected.Message.Date || opened["text"] != nil {
			t.Fatal("image provenance differs or body leaked into image metadata")
		}
		imageMetadata := opened["image"].(map[string]any)
		if imageMetadata["handle"] != descriptor["handle"] || imageMetadata["kind"] != expected.Image.Kind || imageMetadata["mime_type"] != expected.Image.MIMEType || imageMetadata["width"] != float64(expected.Image.Width) || imageMetadata["height"] != float64(expected.Image.Height) || imageMetadata["size"] != float64(len(native.Data)) {
			t.Fatal("image descriptor does not match delivered content")
		}
		effect := content["read_effect"].(map[string]any)
		if effect["kind"] != "history_marked_read" || effect["through_message_id"] != expected.Message.ID.String() || backend.acks.Load() != beforeAcks+1 || backend.downloads.Load() != beforeDownloads+1 {
			t.Fatal("image arrived without its exact receipt and explicit download")
		}
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded) > model.MaximumMediaResultBytes {
			t.Fatal("native result exceeded output budget", err)
		}
	}
	open(photo, backend.candidates[0])
	_, history := call("list_messages", map[string]any{"peer": backend.peer.String()})
	items := history["items"].([]any)
	if len(items) != 2 || history["read_effect"].(map[string]any)["kind"] != "history_marked_read" || backend.acks.Load() != 2 || backend.downloads.Load() != 1 {
		t.Fatal("history did not acknowledge metadata without downloading")
	}
	attachment := items[1].(map[string]any)
	if attachment["text"] != "" || attachment["id"] != backend.candidates[1].Message.ID.String() {
		t.Fatal("history lost the image without a caption")
	}
	if len(backend.data[backend.candidates[1].Message.ID]) <= maximumInputFrameBytes {
		t.Fatal("attachment fixture does not exercise output larger than input frames")
	}
	open(attachment, backend.candidates[1])
	beforeFetches, beforeAcks, beforeDownloads := backend.fetches.Load(), backend.acks.Load(), backend.downloads.Load()
	for _, arguments := range []string{
		`{}`, `{"handle":null}`, `{"handle":""}`, `{"handle":"a","handle":"b"}`,
		`{"handle":"a","private-marker":"private-value"}`, `{"handle":"` + strings.Repeat("a", 4097) + `"}`,
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "open_image", Arguments: json.RawMessage(arguments)})
		if err != nil || !result.IsError || len(result.Content) != 1 {
			t.Fatal("invalid image arguments returned content", err)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || !strings.Contains(text.Text, "invalid_input") || strings.Contains(text.Text, "private-") {
			t.Fatal("invalid image arguments leaked or misclassified")
		}
	}
	lease, err = base.repository.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grant.Images = false
	if err := lease.Save(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "open_image", Arguments: map[string]any{"handle": photo["image"].(map[string]any)["handle"]}})
	if err != nil || !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "policy_denied") || result.StructuredContent != nil {
		t.Fatal("revoked image grant released content", err)
	}
	if backend.fetches.Load() != beforeFetches || backend.acks.Load() != beforeAcks || backend.downloads.Load() != beforeDownloads {
		t.Fatal("denied image request reached Telegram")
	}
	_, denied := call("list_messages", map[string]any{"peer": backend.peer.String()})
	if len(denied["items"].([]any)) != 0 || denied["read_effect"].(map[string]any)["kind"] != "none" || backend.acks.Load() != beforeAcks || backend.downloads.Load() != beforeDownloads {
		t.Fatal("text-only grant disclosed image metadata or captions")
	}
}
