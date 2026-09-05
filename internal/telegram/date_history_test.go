package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestDateCandidatesKeepExcludedTimestampsAndRejectUnorderedHistory(t *testing.T) {
	peer := testSelfPeer(t)
	for _, mode := range []string{"service", "empty", "date_order", "id_order", "crosspeer", "invalid_date"} {
		t.Run(mode, func(t *testing.T) {
			newest := testMessage(20)
			newest.Date = 102
			older := testMessage(19)
			older.Date = 101
			var second tg.MessageClass = older
			switch mode {
			case "service":
				second = &tg.MessageService{ID: 19, Date: 101, PeerID: &tg.PeerUser{UserID: peer.TelegramID()}}
			case "empty":
				second = &tg.MessageEmpty{ID: 19}
			case "date_order":
				older.Date = 103
			case "id_order":
				older.ID = 21
			case "crosspeer":
				older.PeerID = &tg.PeerUser{UserID: 999}
			case "invalid_date":
				older.Date = 0
			}
			rows := make([]model.Candidate, 2)
			err := dateCandidates(peer, &tg.MessagesMessages{Messages: []tg.MessageClass{newest, second}}, rows)
			if mode == "service" || mode == "empty" {
				if err != nil || rows[0].SentAt != 102 {
					t.Fatal("valid history rejected", err)
				}
				if mode == "service" && rows[1].SentAt != 101 {
					t.Fatal("service date lost")
				}
				if mode == "empty" && rows[1].SentAt != 0 {
					t.Fatal("invented empty message date")
				}
			} else if model.TextErrorCategory(err) != model.ErrorInvalidReference {
				t.Fatal("invalid ordering or date accepted")
			}
		})
	}
}
