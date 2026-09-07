package reader

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestAlbumExpansionNarrowsWindowAndAcknowledgesReturnedBoundary(t *testing.T) {
	for _, mode := range []string{"album", "not_album", "denied_member", "receipt", "batch", "bound"} {
		t.Run(mode, func(t *testing.T) {
			s, image, _, g, _ := imageService(t)
			f := image.fakeBackend
			album, _ := model.NewAlbumID(g.Peer, 42)
			rows := []model.Candidate{candidate(g, 18, "older unrelated"), candidate(g, 19, "older caption"), candidate(g, 20, "target caption"), candidate(g, 21, "newer caption"), candidate(g, 22, "newer unrelated")}
			for i := 1; i <= 3; i++ {
				rows[i].Message.AlbumID = album
				rows[i].Image = image.items[0].Image
			}
			if mode == "not_album" {
				rows[2].Message.AlbumID = ""
			}
			if mode == "denied_member" {
				rows[3].Message.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
			}
			b := &batchBackend{fakeBackend: f, rows: map[model.PeerID][]model.Candidate{g.Peer: rows}}
			s.backend = b
			if mode == "receipt" {
				b.failAck = 1
			}
			queries := []model.HistoryQuery{{Peer: g.Peer, Target: 20, Limit: 1, ExpandAlbum: true}}
			if mode == "batch" {
				queries = append(queries, model.HistoryQuery{Peer: g.Peer, Target: 21, Limit: 1, ExpandAlbum: true})
			}
			if mode == "bound" {
				for i := 0; i < 5; i++ {
					q := queries[0]
					q.Target = int32(21 + i)
					queries = append(queries, q)
				}
			}
			result, err := s.Contexts(context.Background(), "req_album_context", queries)
			if mode == "receipt" || mode == "bound" {
				if err == nil || len(result.JSON) != 0 {
					t.Fatal("failed expansion released bodies")
				}
				if mode == "receipt" && model.TextErrorCategory(err) != model.ErrorReadEffectUncertain {
					t.Fatal(err)
				}
				if mode == "bound" && (b.historyCalls != 0 || b.ackCalls != 0) {
					t.Fatal("over-budget expansion performed I/O")
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
			want := 3
			through := int32(21)
			if mode == "not_album" {
				want = 1
				through = 20
			}
			if mode == "denied_member" {
				want = 2
				through = 20
			}
			if len(page.Items) != want || b.ackCalls != 1 || b.receipts[0].TelegramID() != through {
				t.Fatal("unexpected membership or receipt")
			}
			for _, item := range page.Items {
				if item.ID.TelegramID() == 20 {
					if item.AlbumContext == nil {
						t.Fatal("missing coverage")
					}
					state := "bounded"
					members := want
					if mode == "not_album" {
						state = "not_album"
						members = 0
					}
					if item.AlbumContext.State != state || len(item.AlbumContext.Messages) != members {
						t.Fatal("incorrect coverage")
					}
				}
			}
		})
	}
}

func TestAlbumExpansionCountsDeniedNeighborPositions(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "target"), candidate(g, 19, "withheld nearest"), candidate(g, 18, "older unrelated")}
	f.items[1].Message.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
	result, err := s.Messages(context.Background(), "req_album_neighbors", model.HistoryQuery{Peer: g.Peer, Target: 20, BeforeCount: 1, Limit: 2, ExpandAlbum: true})
	if err != nil {
		t.Fatal(err)
	}
	var page model.Envelope[model.Message]
	if err := json.Unmarshal(result.JSON, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || f.query.BeforeCount != 9 || f.query.AfterCount != 9 {
		t.Fatal("denied neighbor shifted selection")
	}
	f.historyError = errors.New("synthetic")
	if result, err := s.Messages(context.Background(), "req_album_fail", model.HistoryQuery{Peer: g.Peer, Target: 20, Limit: 1, ExpandAlbum: true}); err == nil || len(result.JSON) != 0 {
		t.Fatal("failed scan released content")
	}
}

func TestAlbumExpansionRetainsRequestedNonAlbumReplyParent(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	parent := candidate(g, 19, "parent")
	target := candidate(g, 20, "target")
	target.Message.ReplyTo = &parent.Message.ID
	b := &batchBackend{fakeBackend: f, rows: map[model.PeerID][]model.Candidate{g.Peer: {target, parent}}}
	s.backend = b
	result, err := s.Messages(context.Background(), "req_album_reply", model.HistoryQuery{Peer: g.Peer, Target: 20, Limit: 1, ExpandAlbum: true, ReplyDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	var page model.Envelope[model.Message]
	if err := json.Unmarshal(result.JSON, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ReplyChain == nil || page.Items[0].ReplyChain.State != "complete" {
		t.Fatal("discarded neighbor incorrectly hid reply parent")
	}
}
