package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestReplyNormalizationPreservesOnlySafeSamePeerReferences(t *testing.T) {
	for _, scenario := range []string{"ordinary", "thread", "topic", "cross_peer", "quote", "protected", "future", "self", "negative", "present_zero"} {
		t.Run(scenario, func(t *testing.T) {
			peer := testSelfPeer(t)
			m := testMessage(20)
			reply := &tg.MessageReplyHeader{ReplyToMsgID: 10}
			m.ReplyTo = reply
			safe := true
			switch scenario {
			case "thread":
				reply.ReplyToTopID = 5
			case "topic":
				base, _ := model.NewPeerID(model.PeerKindChannel, 42)
				peer, _ = model.NewTopicPeer(base, 7)
				m.PeerID = &tg.PeerChannel{ChannelID: 42}
				reply.ForumTopic = true
				reply.ReplyToTopID = 7
			case "cross_peer":
				reply.ReplyToPeerID = &tg.PeerChat{ChatID: 9}
				safe = false
			case "quote":
				reply.QuoteText = "never expose embedded quote"
				safe = false
			case "protected":
				m.Noforwards = true
				safe = false
			case "future":
				reply.ReplyToMsgID = 21
				safe = false
			case "self":
				reply.ReplyToMsgID = 20
				safe = false
			case "negative":
				reply.ReplyToMsgID = -1
				safe = false
			case "present_zero":
				reply.ReplyToMsgID = 0
				reply.Flags.Set(4)
				safe = false
			}
			c, err := normalizeMessage(peer, 1, m, map[int64]bool{1: true}, false)
			if err != nil {
				t.Fatal(err)
			}
			if safe {
				if c.Message.ReplyTo == nil || c.Message.ReplyTo.Peer() != peer || c.Message.ReplyTo.TelegramID() != 10 || c.Message.Text == "" {
					t.Fatal("reply reference lost")
				}
			} else if c.Message.ReplyTo != nil || c.Message.Text != "" {
				t.Fatal("unsafe reply escaped")
			}
		})
	}
}
