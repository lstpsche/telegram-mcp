package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestUnreadMentionsSearchUsesExactBoundsAndEvidence(t *testing.T) {
	for _, query := range []string{"", "needle"} {
		t.Run(query, func(t *testing.T) {
			calls := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch q := in.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesGetUnreadMentionsRequest:
					calls++
					if _, ok := q.Peer.(*tg.InputPeerSelf); !ok {
						t.Fatal("search widened peer")
					}
					if q.OffsetID != 21 || q.MinID != 9 || q.MaxID != 21 || q.Limit != 2 {
						t.Fatal("wrong mentioned search bounds")
					}
					mentioned := testMessage(20)
					mentioned.Mentioned, mentioned.MediaUnread = true, true
					return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{mentioned, testMessage(18)}})
				default:
					t.Fatalf("unexpected RPC %T", in)
					return nil
				}
			})
			rows, err := account.Search(context.Background(), model.SearchQuery{Peer: testSelfPeer(t), Query: query, UnreadMentionsOnly: true, MinID: 10, MaxID: 20, Limit: 2})
			if err != nil || len(rows) != 2 || calls != 1 {
				t.Fatal("mentioned search failed", err)
			}
			if !rows[0].UnreadMention || rows[1].UnreadMention {
				t.Fatal("mention state fabricated or lost")
			}
		})
	}
}
