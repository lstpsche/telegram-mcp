package reader

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestPollBodiesRespectReceiptAndResponseBudget(t *testing.T) {
	for _, kind := range []string{"success", "receipt_failure", "oversized", "denied"} {
		t.Run(kind, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			saveGrant(t, p, g)
			c := candidate(g, 20, "")
			c.Message.Poll = &model.Poll{Question: "Question", Options: []model.PollOption{{Text: "First"}, {Text: "Second"}}}
			switch kind {
			case "receipt_failure":
				f.ackError = errors.New("synthetic receipt failure")
			case "oversized":
				c.Message.Poll.Options = make([]model.PollOption, 100)
				for i := range c.Message.Poll.Options {
					c.Message.Poll.Options[i].Text = strings.Repeat("<", 4096)
				}
			case "denied":
				c.Message.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
			}
			f.items = []model.Candidate{c}
			result, err := s.Messages(context.Background(), "req_poll", request(g))
			switch kind {
			case "receipt_failure":
				if model.TextErrorCategory(err) != model.ErrorReadEffectUncertain || len(result.JSON) != 0 {
					t.Fatal("receipt failure released poll", err)
				}
			case "oversized":
				if model.TextErrorCategory(err) != model.ErrorResultTooLarge || len(result.JSON) != 0 || f.ackCalls != 0 {
					t.Fatal("oversized poll caused receipt", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				var e model.Envelope[model.Message]
				if err := json.Unmarshal(result.JSON, &e); err != nil {
					t.Fatal(err)
				}
				if kind == "denied" {
					if len(e.Items) != 0 || f.ackCalls != 0 {
						t.Fatal("denied poll escaped")
					}
				} else if len(e.Items) != 1 || e.Items[0].Poll == nil || f.ackCalls != 1 {
					t.Fatal("poll missing")
				}
			}
		})
	}
}
