package telegram

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestTopicRoutesHistorySearchAndReceiptToExactThread(t *testing.T) {
	for _, topicID := range []int32{1, 7} {
		t.Run(fmt.Sprint(topicID), func(t *testing.T) {
			group := syntheticSupergroup()
			group.Forum = true
			parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
			peer, _ := model.NewTopicPeer(parent, topicID)
			read := false
			history, search, receipts := 0, 0, 0
			account, _ := newReadTestAccount(t, func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				topic := &tg.ForumTopic{ID: int(topicID), Peer: &tg.PeerChannel{ChannelID: 42}, Date: 100, Title: "Synthetic topic", TopMessage: 20, UnreadCount: 2, FromID: &tg.PeerUser{UserID: 2}, NotifySettings: tg.PeerNotifySettings{}}
				if read {
					topic.ReadInboxMaxID = 20
				}
				message := &tg.Message{ID: 20, PeerID: &tg.PeerChannel{ChannelID: 42}, FromID: &tg.PeerUser{UserID: 2}, Date: 100, Message: "synthetic topic text"}
				if topicID != 1 {
					message.ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: int(topicID)}
				}
				page := &tg.MessagesChannelMessages{Pts: 12, Messages: []tg.MessageClass{message}, Users: []tg.UserClass{&tg.User{ID: 2}}, Chats: []tg.ChatClass{group}}
				switch q := in.(type) {
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{group}})
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesGetForumTopicsByIDRequest:
					if len(q.Topics) != 1 || q.Topics[0] != int(topicID) {
						t.Fatal("wrong topic metadata")
					}
					return encodeReadResponse(out, &tg.MessagesForumTopics{Pts: 12, Count: 1, Topics: []tg.ForumTopicClass{topic}, Chats: []tg.ChatClass{group}})
				case *tg.MessagesGetRepliesRequest:
					history++
					if q.MsgID != int(topicID) {
						t.Fatal("wrong history thread")
					}
					return encodeReadResponse(out, page)
				case *tg.MessagesSearchRequest:
					search++
					if search == 2 {
						if _, ok := q.Filter.(*tg.InputMessagesFilterPinned); !ok || q.Q != "" {
							t.Fatal("topic pin filter lost")
						}
					}
					if search == 3 {
						if _, ok := q.Filter.(*tg.InputMessagesFilterDocument); !ok || q.Q != "" {
							t.Fatal("topic media filter lost")
						}
					}
					if q.TopMsgID != int(topicID) {
						t.Fatal("unscoped search")
					}
					return encodeReadResponse(out, page)
				case *tg.MessagesReadDiscussionRequest:
					receipts++
					if q.MsgID != int(topicID) || q.ReadMaxID != 20 {
						t.Fatal("wrong receipt scope")
					}
					read = true
					return encodeReadResponse(out, &tg.BoolFalse{})
				default:
					t.Fatalf("unexpected RPC %T", in)
					return nil
				}
			})
			ctx := context.Background()
			if err := account.reads.storage.SetChannelAccessHash(ctx, 1, 42, 12345); err != nil {
				t.Fatal(err)
			}
			messages, err := account.History(ctx, model.HistoryQuery{Peer: peer, MinID: 1, MaxID: 100, Limit: 20})
			if err != nil || len(messages) != 1 || messages[0].Message.ID.Peer() != peer || messages[0].Message.Text != "synthetic topic text" {
				t.Fatal("topic history", err)
			}
			if _, err := account.Search(ctx, model.SearchQuery{Peer: peer, Query: "synthetic", MinID: 1, MaxID: 100, Limit: 20}); err != nil {
				t.Fatal(err)
			}
			if _, err := account.Search(ctx, model.SearchQuery{Peer: peer, PinnedOnly: true, MinID: 1, MaxID: 100, Limit: 20}); err != nil {
				t.Fatal(err)
			}
			if _, err := account.Search(ctx, model.SearchQuery{Peer: peer, MediaType: model.SearchMediaPDF, MinID: 1, MaxID: 100, Limit: 20}); err != nil {
				t.Fatal(err)
			}
			if err := account.Acknowledge(ctx, peer, 20); err != nil {
				t.Fatal(err)
			}
			if err := account.Acknowledge(ctx, parent, 20); model.TextErrorCategory(err) != model.ErrorUnsupportedPeer {
				t.Fatal("forum receipt accepted", err)
			}
			if history != 1 || search != 3 || receipts != 1 {
				t.Fatal(history, search, receipts)
			}
		})
	}
}

func TestTopicNormalizationRejectsCrossTopicBodies(t *testing.T) {
	parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
	peer, _ := model.NewTopicPeer(parent, 7)
	for _, root := range []int{1, 8} {
		message := &tg.Message{ID: 20, PeerID: &tg.PeerChannel{ChannelID: 42}, FromID: &tg.PeerUser{UserID: 2}, Date: 100, Message: "must not escape", ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToTopID: root, ReplyToMsgID: 19}}
		result, err := normalizeMessage(peer, 1, message, map[int64]bool{2: true}, false)
		if err == nil || result.Message.Text != "" {
			t.Fatal("cross-topic content accepted")
		}
	}
}

func TestTopicDiscoveryUsesServerPaginationPosition(t *testing.T) {
	for _, created := range []bool{false, true} {
		t.Run(fmt.Sprint(created), func(t *testing.T) {
			group := syntheticSupergroup()
			group.Forum = true
			parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch q := in.(type) {
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{group}})
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesGetForumTopicsRequest:
					if q.OffsetTopic == 7 {
						return encodeReadResponse(out, &tg.MessagesForumTopics{Pts: 12, Count: 30})
					}
					if q.Limit != 20 || q.OffsetTopic != 0 {
						t.Fatal("unexpected pagination request")
					}
					return encodeReadResponse(out, &tg.MessagesForumTopics{OrderByCreateDate: created, Pts: 12, Count: 30, Topics: []tg.ForumTopicClass{&tg.ForumTopic{ID: 7, Peer: &tg.PeerChannel{ChannelID: 42}, Date: 100, Title: "Synthetic", TopMessage: 20, FromID: &tg.PeerUser{UserID: 2}}}, Messages: []tg.MessageClass{&tg.Message{ID: 20, PeerID: &tg.PeerChannel{ChannelID: 42}, Date: 200, Message: "discarded incidental body"}}, Chats: []tg.ChatClass{group}})
				default:
					t.Fatalf("unexpected RPC %T", in)
					return nil
				}
			})
			ctx := context.Background()
			if err := account.reads.storage.SetChannelAccessHash(ctx, 1, 42, 12345); err != nil {
				t.Fatal(err)
			}
			page, err := account.Topics(ctx, parent, model.TopicPosition{}, 20)
			date := 200
			if created {
				date = 100
			}
			if err != nil || len(page.Items) != 1 || page.Next == nil || *page.Next != (model.TopicPosition{Date: date, Message: 20, Topic: 7}) {
				t.Fatal("short page lost correct continuation", page, err)
			}
			terminal, err := account.Topics(ctx, parent, *page.Next, 20)
			if err != nil || len(terminal.Items) != 0 || terminal.Next != nil {
				t.Fatal("empty terminal page rejected", err)
			}

		})
	}
}

func TestTopicMetadataRejectsDeletedOrUntrustedShapes(t *testing.T) {
	parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
	for _, mutation := range []string{"short", "missing title", "wrong peer", "hidden", "invalid id"} {
		topic := &tg.ForumTopic{ID: 7, Peer: &tg.PeerChannel{ChannelID: 42}, Date: 100, Title: "Synthetic", TopMessage: 20}
		switch mutation {
		case "short":
			topic.Short = true
		case "missing title":
			topic.TitleMissing = true
		case "wrong peer":
			topic.Peer = &tg.PeerChannel{ChannelID: 43}
		case "hidden":
			topic.Hidden = true
		case "invalid id":
			topic.ID = 0
		}
		if validTopic(parent, topic) {
			t.Fatal("invalid topic accepted", mutation)
		}
	}
}
