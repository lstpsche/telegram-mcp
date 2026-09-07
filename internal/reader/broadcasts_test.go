package reader

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func TestChannelPostMutationWithholdsMedia(t *testing.T) {
	for _, when := range []string{"unchanged", "download", "receipt"} {
		t.Run(when, func(t *testing.T) {
			s, f, p, g, _ := imageService(t)
			g.Peer, _ = model.NewPeerID(model.PeerKindChannel, 42)
			g.Author = g.Peer
			g.Profile = policy.ProfileConsented
			saveGrant(t, p, g)
			f.items[0].Message.ID, _ = model.NewMessageID(g.Peer, 20)
			f.items[0].Message.Author = g.Peer
			f.items[0].Message.ChannelPost = &model.ChannelPost{Signature: "original"}
			r, err := s.Search(context.Background(), "req_channel_search", g.Peer, model.SearchFilter{Query: "caption"}, 20, "")
			if err != nil {
				t.Fatal(err)
			}
			var e model.Envelope[model.SearchHit]
			if err := json.Unmarshal(r.JSON, &e); err != nil {
				t.Fatal(err)
			}
			mutate := func() { f.items[0].Message.ChannelPost.Signature = "changed" }
			if when == "download" {
				f.onDownload = mutate
			}
			if when == "receipt" {
				f.onAck = mutate
			}
			r, err = s.OpenImage(context.Background(), "req_channel_image", e.Items[0].Image.Handle)
			if when == "unchanged" {
				if err != nil || r.Image == nil {
					t.Fatal("channel image failed", err)
				}
				return
			}
			if err == nil || r.Image != nil || len(r.JSON) != 0 {
				t.Fatal("mutated publisher metadata escaped")
			}
			for _, b := range f.data {
				if b != 0 {
					t.Fatal("failed bytes retained")
				}
			}
		})
	}
}
