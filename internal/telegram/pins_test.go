package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestPinnedSearchUsesProviderFilterAndNormalizesState(t *testing.T) {
	for _, query := range []string{"", "needle"} {
		t.Run(query, func(t *testing.T) {
			calls := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch q := in.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesSearchRequest:
					calls++
					if _, ok := q.Filter.(*tg.InputMessagesFilterPinned); !ok {
						t.Fatal("missing provider pin filter")
					}
					if _, ok := q.Peer.(*tg.InputPeerSelf); !ok {
						t.Fatal("search widened peer")
					}
					if q.Q != query || q.OffsetID != 21 || q.MinID != 9 || q.MaxID != 21 || q.Limit != 2 || q.Hash != 0 {
						t.Fatal("wrong pinned search bounds")
					}
					pinned := testMessage(20)
					pinned.Pinned = true
					return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{pinned, testMessage(18)}})
				default:
					t.Fatalf("unexpected RPC %T", in)
					return nil
				}
			})
			rows, err := account.Search(context.Background(), model.SearchQuery{Peer: testSelfPeer(t), Query: query, PinnedOnly: true, MinID: 10, MaxID: 20, Limit: 2})
			if err != nil || len(rows) != 2 || calls != 1 {
				t.Fatal("pinned search failed", err)
			}
			if !rows[0].Message.Pinned || rows[1].Message.Pinned {
				t.Fatal("pin state fabricated or lost")
			}
		})
	}
}
