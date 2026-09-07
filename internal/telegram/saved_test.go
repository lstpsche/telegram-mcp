package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestSavedSourceUsesSuppliedGroupingNotForwardAuthor(t *testing.T) {
	for _, source := range []tg.PeerClass{&tg.PeerUser{UserID: 1}, &tg.PeerUser{UserID: 2666000}, &tg.PeerChat{ChatID: 99}, &tg.PeerChannel{ChannelID: 99}, nil} {
		m := testMessage(20)
		m.SetFwdFrom(tg.MessageFwdHeader{Date: 99, FromID: &tg.PeerUser{UserID: 2}, SavedFromPeer: &tg.PeerChat{ChatID: 88}, SavedFromMsgID: 10})
		m.SavedPeerID = source
		c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true, 2: true}, false)
		if err != nil || c.Unsupported || !c.Message.ValidSavedPeer() || c.Message.Author.TelegramID() != 1 {
			t.Fatal("saved normalization failed", err)
		}
		if source == nil {
			if c.Message.SavedPeer != "" {
				t.Fatal("invented source")
			}
		} else if c.Message.SavedPeer == "" || c.Message.SavedPeer == "tgpeer:v1:user:2" || c.Message.SavedPeer == "tgpeer:v1:chat:88" {
			t.Fatal("forward attribution became grouping")
		}
		m.Noforwards = true
		c, err = normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false)
		if err != nil || c.Message.SavedPeer != "" || c.Message.Text != "" {
			t.Fatal("protected source escaped", err)
		}
	}
	for _, source := range []tg.PeerClass{&tg.PeerUser{}, &tg.PeerChannel{}, &tg.PeerChat{ChatID: -1}} {
		m := testMessage(20)
		m.SetFwdFrom(tg.MessageFwdHeader{Date: 99, FromName: "hidden"})
		m.SavedPeerID = source
		if _, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false); err == nil {
			t.Fatal("malformed source accepted")
		}
	}
}

func TestSavedSearchRejectsScopeWidening(t *testing.T) {
	account, _ := newReadTestAccount(t, func(_ context.Context, _ bin.Encoder, _ bin.Decoder) error {
		t.Fatal("invalid selector reached Telegram")
		return nil
	})
	peer, _ := model.NewPeerID(model.PeerKindChat, 42)
	_, err := account.Search(context.Background(), model.SearchQuery{Peer: peer, SavedPeer: "tgpeer:v1:chat:99", MinID: 1, MaxID: 100, Limit: 10})
	if model.TextErrorCategory(err) != model.ErrorInvalidInput {
		t.Fatal("non-saved selector accepted", err)
	}
}
