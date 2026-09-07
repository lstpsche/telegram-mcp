package reader

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestChatTitleSearchPreservesFilteredContinuation(t *testing.T) {
	s, f, p, _, g := testService(t)
	setFullRead(t, p, true)
	position := model.DialogPosition{Peer: g.Peer.String(), MessageID: 20, Date: 100, Pinned: true}
	backend := &fullReadBackend{fakeBackend: f}
	backend.dialog = func(_ context.Context, pos model.DialogPosition, _ int) (model.DialogPage, error) {
		title := "Other"
		var next *model.DialogPosition
		if pos == (model.DialogPosition{}) {
			next = &position
		} else {
			title = "Команда Backend"
		}
		return model.DialogPage{Scanned: 1, Items: []model.DialogEntry{{Chat: model.Chat{ID: g.Peer, Title: title}, Unread: model.Unread{Peer: g.Peer}}}, Next: next}, nil
	}
	s.backend = backend
	result, err := s.Chats(context.Background(), "req_title_first", 1, nil, "", "  КОМАНДА ")
	if err != nil {
		t.Fatal(err)
	}
	var page model.Envelope[model.Chat]
	if err := json.Unmarshal(result.JSON, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor == nil {
		t.Fatal("filtered page lost continuation")
	}
	token := *page.NextCursor
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "команда") {
		t.Fatal("query exposed in cursor")
	}
	for _, q := range []string{"different", ""} {
		denied, err := s.Chats(context.Background(), "req_title_changed", 1, nil, token, q)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(denied.JSON) != 0 || backend.dialogCalls != 1 {
			t.Fatal("query change fetched", err)
		}
	}
	result, err = s.Chats(context.Background(), "req_title_next", 1, nil, token, "команда")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.JSON, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor != nil || page.ReadEffect.Kind != model.ReadEffectNone || f.ackCalls != 0 {
		t.Fatal("matching page contract")
	}
}

func TestTopicTitleSearchPreservesFilteredContinuation(t *testing.T) {
	s, f, p, _, _ := testService(t)
	setFullRead(t, p, true)
	parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
	topic, _ := model.NewTopicPeer(parent, 7)
	backend := &topicFake{fakeBackend: f, page: model.TopicPage{Items: []model.Topic{{ID: topic, Title: "Other"}}, Next: &model.TopicPosition{Date: 100, Message: 20, Topic: 7}}}
	s.backend = backend
	result, err := s.ListTopics(context.Background(), "req_topic_title_first", parent, 1, "", " backend ")
	if err != nil {
		t.Fatal(err)
	}
	var page model.Envelope[model.Topic]
	if err := json.Unmarshal(result.JSON, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor == nil {
		t.Fatal("filtered topic page lost continuation")
	}
	token := *page.NextCursor
	for _, q := range []string{"different", ""} {
		denied, err := s.ListTopics(context.Background(), "req_topic_title_changed", parent, 1, token, q)
		if model.TextErrorCategory(err) != model.ErrorCursorInvalid || len(denied.JSON) != 0 || backend.listed != 1 {
			t.Fatal("changed topic query fetched", err)
		}
	}
	backend.page = model.TopicPage{Items: []model.Topic{{ID: topic, Title: "Backend team"}}}
	result, err = s.ListTopics(context.Background(), "req_topic_title_next", parent, 1, token, "BACKEND")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.JSON, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor != nil || f.ackCalls != 0 {
		t.Fatal("topic title matching failed")
	}
}

func TestRestrictedDiscoveryTitleSearchStaysWithinGrants(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	for _, q := range []string{"SYNTHETIC", "missing"} {
		result, err := s.Chats(context.Background(), "req_restricted_title", 20, nil, "", q)
		if err != nil {
			t.Fatal(err)
		}
		var page model.Envelope[model.Chat]
		if err := json.Unmarshal(result.JSON, &page); err != nil {
			t.Fatal(err)
		}
		want := 0
		if q == "SYNTHETIC" {
			want = 1
		}
		if len(page.Items) != want {
			t.Fatal("restricted title match")
		}
	}
	parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
	g.Peer, _ = model.NewTopicPeer(parent, 7)
	saveGrant(t, p, g)
	b := &topicFake{fakeBackend: f}
	s.backend = b
	result, err := s.ListTopics(context.Background(), "req_restricted_topic_title", parent, 20, "", "TOPIC")
	if err != nil {
		t.Fatal(err)
	}
	var page model.Envelope[model.Topic]
	if err := json.Unmarshal(result.JSON, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || b.listed != 0 || b.exact != 1 || f.ackCalls != 0 {
		t.Fatal("restricted search expanded authority")
	}
}

func TestDiscoveryQueryRejectsInvalidInputBeforeFetch(t *testing.T) {
	s, f, _, _, _ := testService(t)
	for _, q := range []string{" \n ", strings.Repeat("a", 1025), strings.Repeat("界", 257), string([]byte{0xff})} {
		result, err := s.Chats(context.Background(), "req_bad_title", 20, nil, "", q)
		if model.TextErrorCategory(err) != model.ErrorInvalidInput || len(result.JSON) != 0 || f.chatCalls != 0 {
			t.Fatal("invalid query accepted", err)
		}
	}
}
