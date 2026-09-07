package reader

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestLinkPreviewBodiesRespectReceiptAndResponseBudget(t *testing.T) {
	for _, kind := range []string{"success", "receipt_failure", "oversized", "denied", "invalid_preview"} {
		t.Run(kind, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			saveGrant(t, p, g)
			c := candidate(g, 20, "")
			c.Message.LinkPreview = &model.LinkPreview{State: "available", URL: "https://example.invalid", Title: "Title"}
			switch kind {
			case "receipt_failure":
				f.ackError = errors.New("synthetic receipt failure")
			case "oversized":
				c.Message.LinkPreview.URL = strings.Repeat("<", 4096)
				c.Message.LinkPreview.Title = strings.Repeat("<", 4096)
				c.Message.LinkPreview.SiteName = strings.Repeat("<", 4096)
				c.Message.LinkPreview.Description = strings.Repeat("<", 4096)
			case "invalid_preview":
				c.Message.LinkPreview.State = "unknown"
			case "denied":
				c.Message.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
			}
			f.items = []model.Candidate{c}
			if kind == "oversized" {
				for id := int32(19); id >= 17; id-- {
					other := c
					other.Message.ID, _ = model.NewMessageID(g.Peer, id)
					f.items = append(f.items, other)
				}
			}
			result, err := s.Messages(context.Background(), "req_preview", request(g))
			switch kind {
			case "receipt_failure":
				if model.TextErrorCategory(err) != model.ErrorReadEffectUncertain || len(result.JSON) != 0 {
					t.Fatal("receipt failure released preview", err)
				}
			case "oversized":
				if model.TextErrorCategory(err) != model.ErrorResultTooLarge || len(result.JSON) != 0 || f.ackCalls != 0 {
					t.Fatal("oversized preview caused receipt", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				var e model.Envelope[model.Message]
				if err := json.Unmarshal(result.JSON, &e); err != nil {
					t.Fatal(err)
				}
				if kind == "denied" || kind == "invalid_preview" {
					if len(e.Items) != 0 || f.ackCalls != 0 {
						t.Fatal("denied preview escaped")
					}
				} else if len(e.Items) != 1 || e.Items[0].LinkPreview == nil || f.ackCalls != 1 {
					t.Fatal("preview missing")
				}
			}
		})
	}
}
