package telegram

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestForwardCopiesPreserveOriginWithoutChangingContainer(t *testing.T) {
	for _, kind := range []model.PeerKind{model.PeerKindSelf, model.PeerKindUser, model.PeerKindChat, model.PeerKindChannel} {
		peer, _ := model.NewPeerID(kind, 1)
		if kind == model.PeerKindChannel {
			peer, _ = model.NewTopicPeer(peer, 7)
		}
		for _, origin := range []tg.PeerClass{&tg.PeerUser{UserID: 99}, &tg.PeerChannel{ChannelID: 99}, nil} {
			m := testMessage(10)
			switch kind {
			case model.PeerKindSelf, model.PeerKindUser:
				m.PeerID = &tg.PeerUser{UserID: 1}
			case model.PeerKindChat:
				m.PeerID = &tg.PeerChat{ChatID: 1}
			case model.PeerKindChannel:
				m.PeerID = &tg.PeerChannel{ChannelID: 1}
				m.ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 7}
			}
			m.FromID = &tg.PeerUser{UserID: 2}
			if kind == model.PeerKindSelf {
				m.SavedPeerID = &tg.PeerChannel{ChannelID: 99}
			}
			m.SetFwdFrom(tg.MessageFwdHeader{Date: 99, FromID: origin, FromName: "untrusted label"})
			c, err := normalizeMessage(peer, 1, m, map[int64]bool{1: true, 2: true})
			if err != nil || c.Unsupported || c.Message.Text == "" || c.Message.Forward == nil || !c.Forwarded || c.Message.ID.Peer() != peer {
				t.Fatal("forward rejected or misrouted", err)
			}
			author := int64(2)
			if kind == model.PeerKindSelf {
				author = 1
			}
			if c.Message.Author.TelegramID() != author || c.Message.Forward.FromName != "untrusted label" {
				t.Fatal("origin became author")
			}
		}
	}
}

func TestForwardHeadersRejectInvalidOrImportedProvenance(t *testing.T) {
	emptyPSA := tg.MessageFwdHeader{Date: 99}
	emptyPSA.SetPsaType("")
	for _, h := range []tg.MessageFwdHeader{emptyPSA, {}, {Date: 99, Imported: true}, {Date: 99, PsaType: "notice"}, {Date: 99, FromName: strings.Repeat("x", 4097)}, {Date: 99, FromName: string([]byte{255})}, {Date: 99, FromID: &tg.PeerUser{UserID: 0}}} {
		m := testMessage(10)
		m.SetFwdFrom(h)
		c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true})
		if err != nil || !c.Unsupported || c.Message.Text != "" || c.Message.Forward != nil {
			t.Fatal("invalid forward escaped", err)
		}
	}
}

func TestForwardedMediaRetainsOrdinaryProtectionChecks(t *testing.T) {
	for _, m := range []*tg.Message{testPhotoMessage(), testDocumentMessage(), attachmentMessage("application/pdf"), voiceMessage()} {
		m.SetFwdFrom(tg.MessageFwdHeader{Date: 99, FromName: "hidden origin"})
		c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true})
		if err != nil {
			t.Fatal(err)
		}
		if c.Message.Forward == nil || !safeMedia(c) {
			t.Fatal("forwarded media excluded")
		}
		m.Noforwards = true
		c, err = normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true})
		if err != nil {
			t.Fatal(err)
		}
		if safeMedia(c) || c.Message.Forward != nil || c.Message.Text != "" {
			t.Fatal("protected forward escaped")
		}
	}
}
