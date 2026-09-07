package mcpserver

import (
	"bytes"
	"context"
	"encoding/binary"
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

type voiceWireBackend struct{ *documentWireBackend }

func (f *voiceWireBackend) DownloadVoice(_ context.Context, c model.Candidate) ([]byte, error) {
	f.downloads.Add(1)
	return bytes.Clone(f.data[c.Message.ID]), nil
}

func wireVoiceData() []byte {
	page := func(sequence uint32, flags byte, granule uint64, packet []byte) []byte {
		b := make([]byte, 28+len(packet))
		copy(b, "OggS")
		b[5] = flags
		binary.LittleEndian.PutUint64(b[6:14], granule)
		binary.LittleEndian.PutUint32(b[14:18], 123)
		binary.LittleEndian.PutUint32(b[18:22], sequence)
		b[26], b[27] = 1, byte(len(packet))
		copy(b[28:], packet)
		var crc uint32
		for _, v := range b {
			crc ^= uint32(v) << 24
			for bit := 0; bit < 8; bit++ {
				if crc&0x80000000 != 0 {
					crc = crc<<1 ^ 0x04c11db7
				} else {
					crc <<= 1
				}
			}
		}
		binary.LittleEndian.PutUint32(b[22:26], crc)
		return b
	}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8], head[9] = 1, 1
	tags := make([]byte, 16)
	copy(tags, "OpusTags")
	data := append(page(0, 2, 0, head), page(1, 0, 0, tags)...)
	return append(data, page(2, 4, 960, []byte{0xf8, 0xff, 0xfe})...)
}

func TestVoiceOverStdioRelay(t *testing.T) {
	_, base := wireService(t)
	originalPeer := base.peer
	parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
	base.peer, _ = model.NewTopicPeer(parent, 7)
	backend := &voiceWireBackend{newDocumentWireBackend(t, base)}
	c := backend.candidates[0]
	c.Document = nil
	data := wireVoiceData()
	c.Voice = &model.MediaSource{Kind: "voice", MIMEType: "audio/ogg", Size: int64(len(data)), Duration: 1, Fingerprint: strings.Repeat("a", 64)}
	backend.candidates = []model.Candidate{c}
	backend.data = map[model.MessageID][]byte{c.Message.ID: data}
	lease, err := base.repository.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := lease.Grant(context.Background(), originalPeer)
	if err != nil {
		t.Fatal(err)
	}
	grant.Peer = base.peer
	grant.VoiceNotes = true
	if err := lease.Save(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	scope, err := lease.SaveScope(context.Background(), "", "topic", []model.PeerID{base.peer})
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
	client := mcp.NewClient(&mcp.Implementation{Name: "voice-wire-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	topics, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_topics", Arguments: map[string]any{"peer": parent.String()}})
	if err != nil || topics.IsError {
		t.Fatal("topic discovery", err, topics)
	}
	var topicEnvelope model.Envelope[model.Topic]
	if err := json.Unmarshal([]byte(topics.Content[0].(*mcp.TextContent).Text), &topicEnvelope); err != nil || len(topicEnvelope.Items) != 1 || topicEnvelope.Items[0].ID != base.peer {
		t.Fatal("topic identity did not survive relay", err)
	}
	inventory, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want int
	}{
		{"list_topics", map[string]any{"peer": parent.String(), "query": " TOPIC "}, 1},
		{"list_topics", map[string]any{"peer": parent.String(), "query": "absent"}, 0},
		{"list_chats", map[string]any{"scope": scope.ID.String(), "query": "SYNTHETIC"}, 1},
		{"list_chats", map[string]any{"scope": scope.ID.String(), "query": "absent"}, 0},
	} {
		got, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil || got.IsError {
			t.Fatal("title search failed", err, got)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(got.Content[0].(*mcp.TextContent).Text), &value); err != nil {
			t.Fatal(err)
		}
		if len(value["items"].([]any)) != tc.want || !reflect.DeepEqual(value, got.StructuredContent) {
			t.Fatal("title search output contract")
		}
		for _, tool := range inventory.Tools {
			if tool.Name == tc.name {
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
				if err := resolved.Validate(value); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, query := range []any{"", "  ", nil, 7} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_topics", Arguments: map[string]any{"peer": parent.String(), "query": query}})
		if err != nil || !result.IsError {
			t.Fatal("invalid title query accepted", err)
		}
	}
	if backend.acks.Load() != 0 || backend.downloads.Load() != 0 {
		t.Fatal("title search affected media or receipts")
	}

	caught, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catch_up", Arguments: map[string]any{"scope": scope.ID.String(), "since": "2026-09-05T00:00:00Z", "until": "2026-09-06T00:00:00Z"}})
	if err != nil || caught.IsError {
		t.Fatal("topic catch-up", err, caught)
	}
	var hits model.Envelope[model.SearchHit]
	if err := json.Unmarshal([]byte(caught.Content[0].(*mcp.TextContent).Text), &hits); err != nil || len(hits.Items) != 1 || hits.Items[0].Voice == nil || hits.Items[0].ID.Peer() != base.peer {
		t.Fatal("topic voice missing from catch-up", err)
	}

	discovery, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_messages", Arguments: map[string]any{"peer": base.peer.String(), "query": "synthetic"}})
	if err != nil || discovery.IsError {
		t.Fatal("voice discovery", err, discovery)
	}
	var envelope model.Envelope[model.SearchHit]
	if err := json.Unmarshal([]byte(discovery.Content[0].(*mcp.TextContent).Text), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 || envelope.Items[0].Voice == nil || backend.acks.Load() != 0 || backend.downloads.Load() != 0 {
		t.Fatal("invalid discovery")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "open_voice_note", Arguments: map[string]any{"handle": envelope.Items[0].Voice.Handle}})
	if err != nil || result.IsError || len(result.Content) != 2 {
		t.Fatal("voice delivery", err, result)
	}
	audio, ok := result.Content[1].(*mcp.AudioContent)
	if !ok || audio.MIMEType != "audio/ogg" || !bytes.Equal(audio.Data, data) || backend.acks.Load() != 1 || backend.downloads.Load() != 1 {
		t.Fatal("native audio did not survive relay")
	}
}

func (f *voiceWireBackend) Topic(_ context.Context, peer model.PeerID) (model.Topic, error) {
	return model.Topic{ID: peer, Title: "Synthetic topic"}, nil
}
func (f *voiceWireBackend) Topics(_ context.Context, parent model.PeerID, position model.TopicPosition, limit int) (model.TopicPage, error) {
	return model.TopicPage{Items: []model.Topic{{ID: f.peer, Title: "Synthetic topic"}}}, nil
}
