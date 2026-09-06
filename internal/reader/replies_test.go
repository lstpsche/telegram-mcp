package reader

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestReplyChainAuthorizationAndBounds(t *testing.T) {
	for _, scenario := range []string{"complete", "full_read", "limit", "missing", "denied", "protected", "outside", "reuse", "reuse_denied", "failure", "oversized", "receipt", "expired", "cross_peer", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			saveGrant(t, p, g)
			if scenario == "full_read" {
				setFullRead(t, p, true)
			}
			target := candidate(g, 20, "answer")
			parent := candidate(g, 15, "question")
			root := candidate(g, 10, "root")
			target.Message.ReplyTo = &parent.Message.ID
			parent.Message.ReplyTo = &root.Message.ID
			if scenario == "full_read" {
				parent.Message.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
			}
			q := model.HistoryQuery{Peer: g.Peer, Target: 20, Limit: 1, ReplyDepth: 2}
			expectedState, expectedCount, expectedCalls := "complete", 3, 3
			fail := false
			switch scenario {
			case "limit":
				q.ReplyDepth = 1
				expectedState = "depth_limit"
				expectedCount = 2
				expectedCalls = 2
			case "missing", "denied", "protected":
				expectedState = "unavailable"
				expectedCount = 1
				expectedCalls = 2
			case "outside":
				id, _ := model.NewMessageID(g.Peer, 9)
				target.Message.ReplyTo = &id
				expectedState = "unavailable"
				expectedCount = 1
				expectedCalls = 1
			case "reuse_denied":
				q.BeforeCount = 1
				q.Limit = 2
				expectedState = "unavailable"
				expectedCount = 1
				expectedCalls = 1
			case "reuse":
				q.BeforeCount = 1
				q.Limit = 2
				expectedCalls = 2
			case "failure", "oversized", "receipt", "expired", "cross_peer", "cycle":
				fail = true
			}
			if scenario == "denied" || scenario == "reuse_denied" {
				parent.Message.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
			}
			if scenario == "protected" {
				parent.Protected = true
			}
			if scenario == "oversized" {
				parent.Message.Text = strings.Repeat("<", model.MaximumTextResultBytes)
			}
			if scenario == "receipt" {
				f.ackError = errors.New("synthetic receipt failure")
			}
			if scenario == "cycle" {
				parent.Message.ReplyTo = &target.Message.ID
			}
			if scenario == "cross_peer" {
				peer, _ := model.NewPeerID(model.PeerKindChat, 99)
				id, _ := model.NewMessageID(peer, 15)
				target.Message.ReplyTo = &id
			}
			f.onHistory = func() {
				switch f.query.Target {
				case 20:
					f.items = []model.Candidate{target}
					if scenario == "reuse" || scenario == "reuse_denied" {
						f.items = append(f.items, parent)
					}
				case 15:
					if f.query.Peer != g.Peer || f.query.Limit != 1 || (scenario != "full_read" && (f.query.MinID != g.MinID || f.query.MaxID != g.MaxID)) {
						t.Fatal("unbounded parent lookup")
					}
					f.items = []model.Candidate{parent}
					if scenario == "missing" {
						f.items = nil
					}
					if scenario == "failure" {
						f.historyError = errors.New("synthetic failure")
					}
					if scenario == "expired" {
						s.now = func() time.Time { return g.ExpiresAt }
					}
				case 10:
					f.items = []model.Candidate{root}
				default:
					t.Fatal("unexpected target")
				}
			}
			result, err := s.Messages(context.Background(), "req_replies", q)
			if fail {
				if err == nil || len(result.JSON) != 0 {
					t.Fatal("failed chain released bodies", err)
				}
				if scenario != "receipt" && f.ackCalls != 0 {
					t.Fatal("failed chain acknowledged")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var e model.Envelope[model.Message]
			if err := json.Unmarshal(result.JSON, &e); err != nil {
				t.Fatal(err)
			}
			if len(e.Items) != expectedCount || e.Items[0].ReplyChain.State != expectedState || e.Items[0].ReplyChain.Depth != expectedCount-1 || f.historyCalls != expectedCalls || f.ackCalls != 1 || f.through != 20 {
				t.Fatalf("unexpected chain: %s calls=%d", result.JSON, f.historyCalls)
			}
		})
	}
}

func TestReplyDepthValidationAndDefault(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	target := candidate(g, 20, "answer")
	parent, _ := model.NewMessageID(g.Peer, 15)
	target.Message.ReplyTo = &parent
	f.items = []model.Candidate{target}
	for _, q := range []model.HistoryQuery{{Peer: g.Peer, Limit: 1, ReplyDepth: 1}, {Peer: g.Peer, Target: 20, Limit: 1, ReplyDepth: 6}, {Peer: g.Peer, Target: 20, Limit: 1, ReplyDepth: -1}, {Peer: g.Peer, Target: 20, BeforeCount: 49, AfterCount: 49, Limit: 99, ReplyDepth: 2}} {
		if _, err := s.Messages(context.Background(), "req_invalid", q); model.TextErrorCategory(err) != model.ErrorInvalidInput {
			t.Fatal("invalid depth accepted")
		}
	}
	if f.historyCalls != 0 {
		t.Fatal("invalid input fetched content")
	}
	r, err := s.Messages(context.Background(), "req_default", model.HistoryQuery{Peer: g.Peer, Target: 20, Limit: 1})
	if err != nil || f.historyCalls != 1 || strings.Contains(string(r.JSON), "reply_chain") || !strings.Contains(string(r.JSON), "reply_to") {
		t.Fatal("default expanded chain", err)
	}
}

func TestReplyMutationWithholdsMedia(t *testing.T) {
	for _, when := range []string{"unchanged", "download", "receipt"} {
		t.Run(when, func(t *testing.T) {
			s, f, _, g, _ := imageService(t)
			parent, _ := model.NewMessageID(g.Peer, 15)
			f.items[0].Message.ReplyTo = &parent
			r, err := s.Search(context.Background(), "req_reply_search", g.Peer, "caption", 20, "")
			if err != nil {
				t.Fatal(err)
			}
			var e model.Envelope[model.SearchHit]
			if err := json.Unmarshal(r.JSON, &e); err != nil {
				t.Fatal(err)
			}
			if e.Items[0].ReplyTo == nil || *e.Items[0].ReplyTo != parent {
				t.Fatal("search lost reply")
			}
			mutate := func() { *f.items[0].Message.ReplyTo, _ = model.NewMessageID(g.Peer, 14) }
			if when == "download" {
				f.onDownload = mutate
			}
			if when == "receipt" {
				f.onAck = mutate
			}
			r, err = s.OpenImage(context.Background(), "req_reply_image", e.Items[0].Image.Handle)
			if when == "unchanged" {
				if err != nil || r.Image == nil || !strings.Contains(string(r.JSON), parent.String()) {
					t.Fatal("reply image failed", err)
				}
				return
			}
			if err == nil || r.Image != nil || len(r.JSON) != 0 {
				t.Fatal("changed reply escaped")
			}
			for _, b := range f.data {
				if b != 0 {
					t.Fatal("failed bytes retained")
				}
			}
		})
	}
}
