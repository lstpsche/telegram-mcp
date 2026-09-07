package reader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestMessageMetadataRespectsReceiptAndResponseBudget(t *testing.T) {
	for _, feature := range []string{"preview", "reactions"} {
		for _, kind := range []string{"success", "receipt_failure", "oversized", "denied", "invalid_metadata"} {
			t.Run(feature+"/"+kind, func(t *testing.T) {
				s, f, p, _, g := testService(t)
				saveGrant(t, p, g)
				c := candidate(g, 20, "Original")
				if feature == "preview" {
					c.Message.LinkPreview = &model.LinkPreview{State: "available", URL: "https://example.invalid", Title: "Title"}
				} else {
					c.Message.Reactions = &model.Reactions{Counts: []model.ReactionCount{{Kind: "emoji", Emoji: "👍", Count: 2}}}
				}
				switch kind {
				case "receipt_failure":
					f.ackError = errors.New("synthetic receipt failure")
				case "oversized":
					if feature == "preview" {
						c.Message.LinkPreview.URL = strings.Repeat("<", 4096)
						c.Message.LinkPreview.Title = strings.Repeat("<", 4096)
						c.Message.LinkPreview.SiteName = strings.Repeat("<", 4096)
						c.Message.LinkPreview.Description = strings.Repeat("<", 4096)
					} else {
						c.Message.Reactions.Counts = make([]model.ReactionCount, 100)
						for i := range c.Message.Reactions.Counts {
							c.Message.Reactions.Counts[i] = model.ReactionCount{Kind: "emoji", Emoji: fmt.Sprintf("%03d", i) + strings.Repeat("<", 125), Count: 1}
						}
					}
				case "invalid_metadata":
					if feature == "preview" {
						c.Message.LinkPreview.State = "unknown"
					} else {
						c.Message.Reactions.Counts[0].Count = -1
					}
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
				result, err := s.Messages(context.Background(), "req_metadata", request(g))
				switch kind {
				case "receipt_failure":
					if model.TextErrorCategory(err) != model.ErrorReadEffectUncertain || len(result.JSON) != 0 {
						t.Fatal("receipt failure released metadata", err)
					}
				case "oversized":
					if model.TextErrorCategory(err) != model.ErrorResultTooLarge || len(result.JSON) != 0 || f.ackCalls != 0 {
						t.Fatal("oversized metadata caused receipt", err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
					var e model.Envelope[model.Message]
					if err := json.Unmarshal(result.JSON, &e); err != nil {
						t.Fatal(err)
					}
					if kind == "denied" || kind == "invalid_metadata" {
						if len(e.Items) != 0 || f.ackCalls != 0 {
							t.Fatal("denied metadata escaped")
						}
					} else if len(e.Items) != 1 || (feature == "preview" && e.Items[0].LinkPreview == nil) || (feature == "reactions" && e.Items[0].Reactions == nil) || f.ackCalls != 1 {
						t.Fatal("metadata missing")
					}
				}
			})
		}
	}
}
