package reader

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

type publisherFake struct {
	*batchBackend
	calls int
	chat  model.Chat
}

func (b *publisherFake) ResolveMessagePublisher(context.Context, string) (model.Chat, error) {
	b.calls++
	return b.chat, nil
}
func TestLinkContextAuthorityAndIdentity(t *testing.T) {
	for _, mode := range []string{"numeric_restricted", "public_full", "public_restricted", "duplicate_alias", "wrong_type", "topic_full"} {
		t.Run(mode, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			channel, _ := model.NewPeerID(model.PeerKindChannel, 42)
			g.Peer = channel
			link := "https://t.me/c/42/20"
			if mode != "numeric_restricted" {
				link = "https://t.me/synthetic_chat/20"
			}
			forum := mode == "topic_full"
			if forum {
				g.Peer, _ = model.NewTopicPeer(channel, 7)
				link = "https://t.me/synthetic_chat/7/20"
			}
			saveGrant(t, p, g)
			if mode != "numeric_restricted" && mode != "public_restricted" {
				setFullRead(t, p, true)
			}
			b := &publisherFake{batchBackend: &batchBackend{fakeBackend: f, rows: map[model.PeerID][]model.Candidate{g.Peer: {candidate(g, 20, "synthetic")}}}, chat: model.Chat{ID: channel, Forum: forum}}
			if mode == "wrong_type" {
				b.chat.ID, _ = model.NewPeerID(model.PeerKindUser, 42)
			}
			s.backend = b
			q, err := ContextReference(link, model.HistoryQuery{Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			var result Result
			if mode == "duplicate_alias" {
				id, _ := model.NewMessageID(g.Peer, 20)
				other, _ := ContextReference(id.String(), model.HistoryQuery{Limit: 1})
				result, err = s.Contexts(context.Background(), "req_links", []model.HistoryQuery{q, other})
			} else {
				result, err = s.Messages(context.Background(), "req_links", q)
			}
			if mode == "public_restricted" || mode == "wrong_type" || mode == "duplicate_alias" {
				if err == nil || len(result.JSON) != 0 || b.historyCalls != 0 || b.ackCalls != 0 {
					t.Fatal("invalid link context fetched", err)
				}
				if mode == "public_restricted" && b.calls != 0 {
					t.Fatal("restricted username performed I/O")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var page model.Envelope[model.Message]
			if err := json.Unmarshal(result.JSON, &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 1 || page.Items[0].ID.Peer() != g.Peer || page.Items[0].URL != page.Items[0].ID.URL() || b.ackCalls != 1 {
				t.Fatal("link context identity")
			}
		})
	}
}
