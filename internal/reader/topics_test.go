package reader

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

type topicFake struct {
	*fakeBackend
	page          model.TopicPage
	listed, exact int
}

func (f *topicFake) Topics(context.Context, model.PeerID, model.TopicPosition, int) (model.TopicPage, error) {
	f.listed++
	return f.page, nil
}
func (f *topicFake) Topic(_ context.Context, peer model.PeerID) (model.Topic, error) {
	f.exact++
	return model.Topic{ID: peer, Title: "Synthetic topic"}, nil
}

func TestRestrictedTopicsDiscoverOnlyExactGrants(t *testing.T) {
	s, f, p, _, g := testService(t)
	parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
	g.Peer = parent
	saveGrant(t, p, g)
	topic, _ := model.NewTopicPeer(parent, 7)
	backend := &topicFake{fakeBackend: f}
	s.backend = backend
	result, err := s.ListTopics(context.Background(), "req_topics_parent", parent, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[model.Topic]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 0 || backend.listed != 0 || backend.exact != 0 {
		t.Fatal("parent grant expanded to topics")
	}
	g.Peer = topic
	saveGrant(t, p, g)
	result, err = s.ListTopics(context.Background(), "req_topics_exact", parent, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 || envelope.Items[0].ID != topic || backend.listed != 0 || backend.exact != 1 || f.ackCalls != 0 {
		t.Fatal("incorrect restricted discovery")
	}
}

func TestTopicCursorBindsForumLimitPolicyAndLifetime(t *testing.T) {
	for _, change := range []string{"forum", "limit", "policy", "expiry", "tamper"} {
		t.Run(change, func(t *testing.T) {
			s, f, p, _, _ := testService(t)
			setFullRead(t, p, true)
			parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
			topic, _ := model.NewTopicPeer(parent, 7)
			backend := &topicFake{fakeBackend: f, page: model.TopicPage{Items: []model.Topic{{ID: topic, Title: "Synthetic"}}, Next: &model.TopicPosition{Date: 100, Message: 20, Topic: 7}}}
			s.backend = backend
			result, err := s.ListTopics(context.Background(), "req_topics_first", parent, 20, "")
			if err != nil {
				t.Fatal(err)
			}
			var envelope model.Envelope[model.Topic]
			if err := json.Unmarshal(result.JSON, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.NextCursor == nil {
				t.Fatal("missing continuation")
			}
			token := *envelope.NextCursor
			limit := 20
			switch change {
			case "forum":
				parent, _ = model.NewPeerID(model.PeerKindChannel, 43)
			case "limit":
				limit = 10
			case "policy":
				setFullRead(t, p, false)
				setFullRead(t, p, true)
			case "expiry":
				now := s.now()
				s.now = func() time.Time { return now.Add(5 * time.Minute) }
			case "tamper":
				token += "x"
			}
			result, err = s.ListTopics(context.Background(), "req_topics_denied", parent, limit, token)
			if err == nil || len(result.JSON) != 0 || backend.listed != 1 || f.ackCalls != 0 {
				t.Fatal("invalid cursor fetched or exposed metadata", err)
			}
		})
	}
}
