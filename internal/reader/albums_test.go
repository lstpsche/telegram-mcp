package reader

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestAlbumMembersPaginateAndAuthorizeIndependently(t *testing.T) {
	s, f, p, g, _ := imageService(t)
	album, _ := model.NewAlbumID(g.Peer, 9007199254740993)
	first := f.items[0]
	first.Message.AlbumID = album
	first.Message.Text = "album caption"
	second := first
	second.Message.ID, _ = model.NewMessageID(g.Peer, 19)
	second.Message.Text = ""
	denied := first
	denied.Message.ID, _ = model.NewMessageID(g.Peer, 18)
	denied.Message.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
	denied.Message.Text = "withheld caption"
	f.onHistory = func() {
		if f.query.Before == 0 {
			f.items = []model.Candidate{first}
		} else {
			f.items = []model.Candidate{second, denied}
		}
	}
	for _, before := range []int32{0, 20} {
		r, err := s.Messages(context.Background(), "req_album", model.HistoryQuery{Peer: g.Peer, Before: before, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		var e model.Envelope[model.Message]
		if err := json.Unmarshal(r.JSON, &e); err != nil {
			t.Fatal(err)
		}
		if len(e.Items) != 1 || e.Items[0].AlbumID != album {
			t.Fatal("album paging widened authority")
		}
		if before == 0 && e.Items[0].Text != "album caption" {
			t.Fatal("caption lost")
		}
		if before != 0 && e.Items[0].Text != "" {
			t.Fatal("caption copied between members")
		}
	}
	if f.historyCalls != 2 || f.downloads != 0 {
		t.Fatal("album caused extra I/O")
	}
	g.Images = false
	saveGrant(t, p, g)
	r, err := s.Messages(context.Background(), "req_album_denied", model.HistoryQuery{Peer: g.Peer, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	var e model.Envelope[model.Message]
	if err := json.Unmarshal(r.JSON, &e); err != nil {
		t.Fatal(err)
	}
	if len(e.Items) != 0 {
		t.Fatal("album bypassed media permission")
	}
}

func TestAlbumMutationWithholdsMedia(t *testing.T) {
	for _, when := range []string{"unchanged", "download", "receipt"} {
		t.Run(when, func(t *testing.T) {
			s, f, _, g, _ := imageService(t)
			album, _ := model.NewAlbumID(g.Peer, 1)
			f.items[0].Message.AlbumID = album
			r, err := s.Search(context.Background(), "req_album_search", g.Peer, model.SearchFilter{Query: "caption"}, 20, "")
			if err != nil {
				t.Fatal(err)
			}
			var e model.Envelope[model.SearchHit]
			if err := json.Unmarshal(r.JSON, &e); err != nil {
				t.Fatal(err)
			}
			if e.Items[0].AlbumID != album {
				t.Fatal("search lost membership")
			}
			mutate := func() { f.items[0].Message.AlbumID, _ = model.NewAlbumID(g.Peer, 2) }
			if when == "download" {
				f.onDownload = mutate
			}
			if when == "receipt" {
				f.onAck = mutate
			}
			r, err = s.OpenImage(context.Background(), "req_album_image", e.Items[0].Image.Handle)
			if when == "unchanged" {
				var opened model.Envelope[imageItem]
				if err != nil || r.Image == nil {
					t.Fatal("album open failed", err)
				}
				if err := json.Unmarshal(r.JSON, &opened); err != nil {
					t.Fatal(err)
				}
				if opened.Items[0].AlbumID != album {
					t.Fatal("open lost album")
				}
				return
			}
			if err == nil || len(r.JSON) != 0 || r.Image != nil {
				t.Fatal("changed album escaped")
			}
			for _, b := range f.data {
				if b != 0 {
					t.Fatal("failed bytes retained")
				}
			}
		})
	}
}
