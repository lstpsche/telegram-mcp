package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

type documentWireBackend struct {
	*wireBackend
	candidates []model.Candidate
	data       map[model.MessageID][]byte
	downloads  atomic.Int32
}

func newDocumentWireBackend(t *testing.T, base *wireBackend) *documentWireBackend {
	t.Helper()
	backend := &documentWireBackend{wireBackend: base, data: make(map[model.MessageID][]byte)}
	for _, spec := range []struct {
		id   int32
		mime string
		data []byte
	}{
		{20, "application/pdf", syntheticPDF()},
		{18, "text/plain", []byte(strings.Repeat("<", model.MaximumTextAttachmentBytes-32) + "\nПривет\r\n")},
	} {
		id, err := model.NewMessageID(base.peer, spec.id)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(spec.data)
		backend.candidates = append(backend.candidates, model.Candidate{SentAt: 1788609600, Message: model.Message{ID: id, Author: base.author, Date: "2026-09-05T12:00:00Z"}, Document: &model.MediaSource{Kind: "document", MIMEType: spec.mime, Size: int64(len(spec.data)), Fingerprint: hex.EncodeToString(digest[:])}})
		backend.data[id] = spec.data
	}
	return backend
}

func (f *documentWireBackend) History(_ context.Context, query model.HistoryQuery) ([]model.Candidate, error) {
	f.fetches.Add(1)
	if query.Peer != f.peer {
		return nil, errors.New("unexpected document peer")
	}
	var candidates []model.Candidate
	for _, candidate := range f.candidates {
		if query.Target == 0 || candidate.Message.ID.TelegramID() == query.Target {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func (f *documentWireBackend) Search(_ context.Context, query model.SearchQuery) ([]model.Candidate, error) {
	f.fetches.Add(1)
	if query.Peer != f.peer || (query.Window == nil && query.Query != "synthetic") {
		return nil, errors.New("unexpected document search")
	}
	if query.Window != nil {
		if query.Query != "" || !query.Window.Contains(f.candidates[0].Message.Date) {
			return nil, errors.New("unexpected document date window")
		}
		return f.candidates, nil
	}
	return []model.Candidate{f.candidates[0]}, nil
}

func (f *documentWireBackend) DownloadDocument(_ context.Context, expected model.Candidate) ([]byte, error) {
	f.downloads.Add(1)
	for _, candidate := range f.candidates {
		if candidate.Message.ID == expected.Message.ID && candidate.Message.Author == expected.Message.Author && expected.Document != nil && *candidate.Document == *expected.Document {
			return bytes.Clone(f.data[candidate.Message.ID]), nil
		}
	}
	return nil, errors.New("unexpected document source")
}

func (f *documentWireBackend) Acknowledge(_ context.Context, peer model.PeerID, through int32) error {
	if peer != f.peer || (through != 20 && through != 18) {
		return errors.New("unexpected document receipt")
	}
	f.acks.Add(1)
	return nil
}

func TestDocumentsOverStdioRelay(t *testing.T) {
	_, base := wireService(t)
	backend := newDocumentWireBackend(t, base)
	lease, err := base.repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := lease.Grant(context.Background(), base.peer)
	if err != nil {
		t.Fatal(err)
	}
	grant.Documents = true
	if err := lease.Save(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	scope, err := lease.SaveScope(context.Background(), "", "documents", []model.PeerID{base.peer})
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
	client := mcp.NewClient(&mcp.Implementation{Name: "document-wire-test", Version: "1"}, nil)
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
		if tool.Name == "open_document" && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint) {
			t.Fatal("document tool misrepresents its read effects")
		}
	}
	if len(schemas) != 12 || schemas["open_document"] == nil {
		t.Fatal("missing native document tool")
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
			if strings.Contains(text.Text, candidate.Document.Fingerprint) {
				t.Fatal("adapter fingerprint leaked into metadata")
			}
		}
		if name != "open_document" && len(result.Content) != 1 {
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
	pdf := search["items"].([]any)[0].(map[string]any)
	if search["read_effect"].(map[string]any)["kind"] != "none" || backend.acks.Load() != 0 || backend.downloads.Load() != 0 || pdf["snippet"] != "" {
		t.Fatal("document search changed read state, downloaded media, or lost caption")
	}
	_, catchUp := call("catch_up", map[string]any{"scope": discovered["id"], "since": "2026-09-05T00:00:00Z", "until": "2026-09-06T00:00:00Z"})
	caught := catchUp["items"].([]any)
	if len(caught) != len(backend.candidates) || backend.acks.Load() != 0 || backend.downloads.Load() != 0 {
		t.Fatal("catch-up document discovery changed state or lost attachments")
	}
	pdf = caught[0].(map[string]any)
	open := func(item map[string]any, expected model.Candidate) {
		t.Helper()
		descriptor := item["document"].(map[string]any)
		beforeAcks, beforeDownloads := backend.acks.Load(), backend.downloads.Load()
		result, content := call("open_document", map[string]any{"handle": descriptor["handle"]})
		if len(result.Content) != 2 {
			t.Fatal("expected exactly one metadata block and one native document")
		}
		var data []byte
		if expected.Document.MIMEType == "application/pdf" {
			native, ok := result.Content[1].(*mcp.EmbeddedResource)
			if !ok || native.Resource.MIMEType != "application/pdf" || !strings.HasPrefix(native.Resource.URI, "telegram-document:doc1.") || native.Resource.Text != "" {
				t.Fatal("PDF resource missing")
			}
			data = native.Resource.Blob
		} else {
			native, ok := result.Content[1].(*mcp.TextContent)
			if !ok {
				t.Fatal("text attachment is not agent-visible text")
			}
			data = []byte(native.Text)
		}
		if !bytes.Equal(data, backend.data[expected.Message.ID]) {
			t.Fatal("document content changed")
		}

		opened := content["items"].([]any)[0].(map[string]any)
		if opened["id"] != expected.Message.ID.String() || opened["author"] != expected.Message.Author.String() || opened["date"] != expected.Message.Date || opened["text"] != nil {
			t.Fatal("document provenance differs or body leaked into document metadata")
		}
		documentMetadata := opened["document"].(map[string]any)
		if documentMetadata["handle"] != descriptor["handle"] || documentMetadata["mime_type"] != expected.Document.MIMEType || documentMetadata["size"] != float64(len(data)) {
			t.Fatal("document descriptor does not match delivered content")
		}
		effect := content["read_effect"].(map[string]any)
		if effect["kind"] != "history_marked_read" || effect["through_message_id"] != expected.Message.ID.String() || backend.acks.Load() != beforeAcks+1 || backend.downloads.Load() != beforeDownloads+1 {
			t.Fatal("document arrived without its exact receipt and explicit download")
		}
		encoded, err := json.Marshal(result)
		if err != nil || (expected.Document.MIMEType != "application/pdf" && len(encoded) > model.MaximumMediaResultBytes) {
			t.Fatal("native result exceeded output budget", err)
		}
	}
	open(pdf, backend.candidates[0])
	_, history := call("list_messages", map[string]any{"peer": backend.peer.String()})
	items := history["items"].([]any)
	if len(items) != 2 || history["read_effect"].(map[string]any)["kind"] != "history_marked_read" || backend.acks.Load() != 2 || backend.downloads.Load() != 1 {
		t.Fatal("history did not acknowledge metadata without downloading")
	}
	attachment := items[1].(map[string]any)
	if attachment["text"] != "" || attachment["id"] != backend.candidates[1].Message.ID.String() {
		t.Fatal("history lost the document without a caption")
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
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "open_document", Arguments: json.RawMessage(arguments)})
		if err != nil || !result.IsError || len(result.Content) != 1 {
			t.Fatal("invalid document arguments returned content", err)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || !strings.Contains(text.Text, "invalid_input") || strings.Contains(text.Text, "private-") {
			t.Fatal("invalid document arguments leaked or misclassified")
		}
	}
	lease, err = base.repository.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grant.Documents = false
	if err := lease.Save(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "open_document", Arguments: map[string]any{"handle": pdf["document"].(map[string]any)["handle"]}})
	if err != nil || !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "policy_denied") || result.StructuredContent != nil {
		t.Fatal("revoked document grant released content", err)
	}
	if backend.fetches.Load() != beforeFetches || backend.acks.Load() != beforeAcks || backend.downloads.Load() != beforeDownloads {
		t.Fatal("denied document request reached Telegram")
	}
	_, denied := call("list_messages", map[string]any{"peer": backend.peer.String()})
	if len(denied["items"].([]any)) != 0 || denied["read_effect"].(map[string]any)["kind"] != "none" || backend.acks.Load() != beforeAcks || backend.downloads.Load() != beforeDownloads {
		t.Fatal("text-only grant disclosed document metadata or captions")
	}
}

// A minimal complete PDF with a correct cross-reference table, generated locally.
func syntheticPDF() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	b.WriteString("%" + strings.Repeat("x", (3<<20)-1000) + "\n")
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>"}
	offsets := []int{}
	for i, object := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 4\n0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}
