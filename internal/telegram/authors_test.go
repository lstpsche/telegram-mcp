package telegram

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestBotAuthorsUseTheSameMessageBoundaryAsHumans(t *testing.T) {
	for _, conversation := range []string{"group", "topic"} {
		topic := conversation == "topic"
		for _, state := range []string{"human", "bot", "deleted", "minimal", "restricted", "protected"} {
			t.Run(state+"/"+conversation, func(t *testing.T) {
				account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("unexpected RPC"); return nil })
				parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
				peer := parent
				group := syntheticSupergroup()
				message := &tg.Message{ID: 20, PeerID: &tg.PeerChannel{ChannelID: 42}, FromID: &tg.PeerUser{UserID: 2}, Date: 100, Message: "synthetic authored text"}
				if topic {
					group.Forum = true
					peer, _ = model.NewTopicPeer(parent, 7)
					message.ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 7}
				}
				author := &tg.User{ID: 2, Bot: state != "human"}
				switch state {
				case "deleted":
					author.Deleted = true
				case "minimal":
					author.Min = true
				case "restricted":
					author.Restricted = true
				case "protected":
					message.Noforwards = true
				}
				page := &tg.MessagesChannelMessages{Pts: 12, Messages: []tg.MessageClass{message}, Users: []tg.UserClass{author}, Chats: []tg.ChatClass{group}}
				rows, err := account.reads.normalizePage(context.Background(), peer, page, 1)
				if err != nil || len(rows) != 1 {
					t.Fatal(err)
				}
				allowed := state == "human" || state == "bot"
				if (rows[0].Message.Text != "") != allowed {
					t.Fatal("incorrect author/content boundary", state)
				}
				if allowed && (rows[0].Message.Author.TelegramID() != 2 || rows[0].Message.ID.Peer() != peer) {
					t.Fatal("author or topic identity changed")
				}
			})
		}
	}
}
