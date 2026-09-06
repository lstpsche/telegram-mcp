package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestBroadcastPublisherAndDisplayedSender(t *testing.T) {
	peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
	for _, sender := range []tg.PeerClass{nil, &tg.PeerUser{UserID: 2}, &tg.PeerChannel{ChannelID: 99}} {
		m := &tg.Message{ID: 7, Post: true, PeerID: &tg.PeerChannel{ChannelID: 42}, FromID: sender, Date: 100, Message: "post", PostAuthor: "untrusted signature"}
		c, err := normalizeMessage(peer, 1, m, map[int64]bool{2: true}, true)
		if err != nil || c.Unsupported || c.Message.Author != peer || c.Message.ChannelPost == nil || c.Message.ChannelPost.Signature != m.PostAuthor {
			t.Fatal("publisher normalization", err)
		}
		c, err = normalizeMessage(peer, 1, m, map[int64]bool{2: true}, false)
		if err != nil || !c.Unsupported || c.Message.Text != "" || c.Message.ChannelPost != nil {
			t.Fatal("post flag promoted supergroup", err)
		}
		m.Noforwards = true
		c, err = normalizeMessage(peer, 1, m, map[int64]bool{2: true}, true)
		if err != nil || c.Message.Text != "" || c.Message.ChannelPost != nil {
			t.Fatal("protected post exposed", err)
		}
	}
}

func TestBroadcastEligibility(t *testing.T) {
	for _, flag := range []string{"joined", "left", "min", "protected", "restricted", "forum", "megagroup", "monoforum", "gigagroup"} {
		c := syntheticSupergroup()
		c.Megagroup = false
		c.Broadcast = true
		switch flag {
		case "left":
			c.Left = true
		case "min":
			c.Min = true
		case "protected":
			c.Noforwards = true
		case "restricted":
			c.Restricted = true
		case "forum":
			c.Forum = true
		case "megagroup":
			c.Megagroup = true
		case "monoforum":
			c.Monoforum = true
		case "gigagroup":
			c.Gigagroup = true
		}
		if ordinaryBroadcast(c) != (flag == "joined") {
			t.Fatalf("incorrect eligibility for %s", flag)
		}
	}
}

func TestBroadcastAttachmentsRetainPublisher(t *testing.T) {
	peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
	for _, m := range []*tg.Message{testPhotoMessage(), attachmentMessage("application/pdf"), voiceMessage()} {
		m.PeerID = &tg.PeerChannel{ChannelID: 42}
		m.FromID = nil
		m.Post = true
		c, err := normalizeMessage(peer, 1, m, nil, true)
		if err != nil || !safeMedia(c) || c.Message.Author != peer || c.Message.ChannelPost == nil {
			t.Fatal("channel attachment excluded", err)
		}
	}
}
