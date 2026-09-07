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

type batchBackend struct {
	*fakeBackend
	rows     map[model.PeerID][]model.Candidate
	receipts []model.MessageID
	failPeer model.PeerID
	failAck  int
}

func (b *batchBackend) History(ctx context.Context, q model.HistoryQuery) ([]model.Candidate, error) {
	b.historyCalls++
	if b.onHistory != nil {
		b.onHistory()
	}
	if q.Peer == b.failPeer {
		return nil, errors.New("synthetic fetch failure")
	}
	var out []model.Candidate
	for _, c := range b.rows[q.Peer] {
		n := c.Message.ID.TelegramID()
		if n >= q.Target-int32(q.BeforeCount) && n <= q.Target+int32(q.AfterCount) {
			out = append(out, c)
		}
	}
	return out, nil
}
func (b *batchBackend) Acknowledge(ctx context.Context, p model.PeerID, n int32) error {
	b.ackCalls++
	id, _ := model.NewMessageID(p, n)
	b.receipts = append(b.receipts, id)
	if b.onAck != nil {
		b.onAck()
	}
	if b.ackCalls == b.failAck {
		return errors.New("synthetic receipt failure")
	}
	return nil
}
func TestBatchContextPreparationAndReceipts(t *testing.T) {
	for _, mode := range []string{"success", "denied", "fetch", "oversized", "receipt", "expire", "audit", "duplicate", "bound"} {
		t.Run(mode, func(t *testing.T) {
			s, f, p, db, g := testService(t)
			saveGrant(t, p, g)
			h := g
			h.Peer, _ = model.NewPeerID(model.PeerKindChat, 43)
			if mode != "denied" {
				saveGrant(t, p, h)
			}
			b := &batchBackend{fakeBackend: f, rows: map[model.PeerID][]model.Candidate{g.Peer: {candidate(g, 20, "first"), candidate(g, 21, "second")}, h.Peer: {candidate(h, 20, "third")}}}
			s.backend = b
			queries := []model.HistoryQuery{{Peer: g.Peer, Target: 20, AfterCount: 1, Limit: 2}, {Peer: g.Peer, Target: 21, Limit: 1}, {Peer: h.Peer, Target: 20, Limit: 1}}
			switch mode {
			case "fetch":
				b.failPeer = h.Peer
			case "oversized":
				b.rows[h.Peer][0].Message.Text = strings.Repeat("&", 30000)
			case "receipt":
				b.failAck = 2
			case "expire":
				b.onAck = func() { s.now = func() time.Time { return g.ExpiresAt.Add(time.Second) } }
			case "audit":
				if _, err := db.Exec("CREATE TRIGGER deny_batch_audit BEFORE INSERT ON text_audit BEGIN SELECT RAISE(FAIL, 'synthetic'); END"); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				queries[1] = queries[0]
			case "bound":
				queries[0].BeforeCount = 49
				queries[0].AfterCount = 49
				queries[0].Limit = 99
			}
			result, err := s.Contexts(context.Background(), "req_batch", queries)
			if mode != "success" {
				if err == nil || len(result.JSON) != 0 {
					t.Fatal("failed batch released bodies", err)
				}
				if mode == "receipt" || mode == "expire" || mode == "audit" {
					if model.TextErrorCategory(err) != model.ErrorReadEffectUncertain {
						t.Fatal("missing uncertain receipt", err)
					}
				} else if b.ackCalls != 0 {
					t.Fatal("preparation failure acknowledged")
				}
				if (mode == "denied" || mode == "duplicate" || mode == "bound") && b.historyCalls != 0 {
					t.Fatal("invalid batch fetched")
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
			if len(page.Items) != 3 || len(page.Contexts) != 3 || len(page.Contexts[0].Messages) != 2 || len(page.ReadEffect.ThroughMessageIDs) != 2 || b.ackCalls != 2 || b.receipts[0].TelegramID() != 21 || b.receipts[1].TelegramID() != 20 {
				t.Fatal("batch grouping or coalesced receipts incorrect")
			}
			var count int
			if err := db.QueryRow("SELECT item_count FROM text_audit").Scan(&count); err != nil || count != 3 {
				t.Fatal("batch audit", count, err)
			}
		})
	}
}

func TestBatchOverlapPreservesTargetMetadataAndRejectsConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply_metadata", true: "changed_body"}[conflict], func(t *testing.T) {
			s, f, p, _, g := testService(t)
			saveGrant(t, p, g)
			parent := candidate(g, 20, "parent")
			child := candidate(g, 21, "child")
			child.Message.ReplyTo = &parent.Message.ID
			b := &batchBackend{fakeBackend: f, rows: map[model.PeerID][]model.Candidate{g.Peer: {parent, child}}}
			s.backend = b
			if conflict {
				b.onHistory = func() {
					if b.historyCalls == 2 {
						b.rows[g.Peer][0].Message.Text = "changed"
					}
				}
			}
			result, err := s.Contexts(context.Background(), "req_overlap", []model.HistoryQuery{{Peer: g.Peer, Target: 20, AfterCount: 1, Limit: 2, ReplyDepth: 1}, {Peer: g.Peer, Target: 21, BeforeCount: 1, Limit: 2, ReplyDepth: 1}})
			if conflict {
				if model.TextErrorCategory(err) != model.ErrorInvalidReference || len(result.JSON) != 0 || b.ackCalls != 0 {
					t.Fatal("conflicting window released", err)
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
			if len(page.Items) != 2 || page.Items[0].ReplyChain == nil || page.Items[0].ReplyChain.Depth != 1 || page.Items[1].ReplyChain == nil || b.ackCalls != 1 {
				t.Fatal("target reply metadata lost")
			}
		})
	}
}
