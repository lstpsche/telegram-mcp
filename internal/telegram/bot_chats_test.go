package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestPrivateBotChatDiscoveryReadsAndReceipts(t *testing.T) {
	bot := &tg.User{ID: 42, Bot: true, FirstName: "Synthetic bot"}
	bot.SetAccessHash(12345)
	peer, _ := model.NewPeerID(model.PeerKindUser, 42)
	message := &tg.Message{ID: 20, PeerID: &tg.PeerUser{UserID: 42}, FromID: &tg.PeerUser{UserID: 42}, Date: 100, Message: "Synthetic bot response", ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonCallback{Text: "Do not execute", Data: []byte("untrusted callback")}}}}}}
	history, search, receipts := 0, 0, 0
	account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch q := in.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesGetDialogsRequest:
			return encodeReadResponse(out, &tg.MessagesDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 42}, TopMessage: 20}}, Messages: []tg.MessageClass{message}, Users: []tg.UserClass{bot}})
		case *tg.UsersGetUsersRequest:
			return encodeReadResponse(out, &tg.UserClassVector{Elems: []tg.UserClass{bot}})
		case *tg.MessagesGetHistoryRequest:
			history++
			return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{message}, Users: []tg.UserClass{bot}})
		case *tg.MessagesSearchRequest:
			search++
			return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{message}, Users: []tg.UserClass{bot}})
		case *tg.MessagesReadHistoryRequest:
			p, ok := q.Peer.(*tg.InputPeerUser)
			if !ok || p.UserID != 42 || q.MaxID != 20 {
				t.Fatal("wrong bot receipt")
			}
			receipts++
			return encodeReadResponse(out, &tg.MessagesAffectedMessages{Pts: 10, PtsCount: 0})
		default:
			t.Fatalf("unexpected RPC %T", in)
			return nil
		}
	})
	ctx := context.Background()
	page, err := account.Dialogs(ctx, model.DialogPosition{}, 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].Chat.ID != peer {
		t.Fatal("bot discovery failed", err)
	}
	rows, err := account.History(ctx, model.HistoryQuery{Peer: peer, MinID: 1, MaxID: 100, Limit: 5})
	if err != nil || len(rows) != 1 || rows[0].Message.Text != message.Message || rows[0].Message.Author != peer {
		t.Fatal("bot history failed", err)
	}
	rows, err = account.Search(ctx, model.SearchQuery{Peer: peer, Query: "Synthetic", MinID: 1, MaxID: 100, Limit: 5})
	if err != nil || len(rows) != 1 || rows[0].Message.Text != message.Message {
		t.Fatal("bot search failed", err)
	}
	if err := account.Acknowledge(ctx, peer, 20); err != nil {
		t.Fatal(err)
	}
	if history != 1 || search != 1 || receipts != 1 {
		t.Fatal("missing bot read operation")
	}
}
