package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestDiscussionMappingValidatesDestinationAndForwardOrigin(t *testing.T) {
	for _, mode := range []string{"valid", "empty", "wrong_peer", "wrong_origin", "wrong_post", "protected_root", "left", "forum", "upstream", "missing_entity", "recheck"} {
		t.Run(mode, func(t *testing.T) {
			source, _ := model.NewPeerID(model.PeerKindChannel, 42)
			destination, _ := model.NewPeerID(model.PeerKindChannel, 99)
			post, _ := model.NewMessageID(source, 20)
			channel := syntheticSupergroup()
			channel.Megagroup = false
			channel.Broadcast = true
			group := syntheticSupergroup()
			group.ID = 99
			if mode == "left" {
				group.Left = true
			}
			if mode == "forum" {
				group.Forum = true
			}
			root := &tg.Message{ID: 100, PeerID: &tg.PeerChannel{ChannelID: 99}, Date: 100, Message: "incidental root body", FwdFrom: tg.MessageFwdHeader{FromID: &tg.PeerChannel{ChannelID: 42}, ChannelPost: 20, Date: 100}}
			switch mode {
			case "wrong_peer":
				root.PeerID = &tg.PeerChannel{ChannelID: 98}
			case "wrong_origin":
				root.FwdFrom.FromID = &tg.PeerChannel{ChannelID: 41}
			case "wrong_post":
				root.FwdFrom.ChannelPost = 19
			case "protected_root":
				root.Noforwards = true
			}
			calls := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch q := input.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.ChannelsGetChannelsRequest:
					id := q.ID[0].(*tg.InputChannel).ChannelID
					entity := channel
					if id == 99 {
						entity = group
					}
					if calls > 0 && mode == "recheck" {
						return errors.New("synthetic recheck failure")
					}
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{entity}})
				case *tg.MessagesGetDiscussionMessageRequest:
					calls++
					if q.MsgID != 20 || q.Peer.(*tg.InputPeerChannel).ChannelID != 42 {
						t.Fatal("wrong discussion request")
					}
					if mode == "upstream" {
						return errors.New("synthetic discussion failure")
					}
					response := &tg.MessagesDiscussionMessage{Messages: []tg.MessageClass{root}, Chats: []tg.ChatClass{group}}
					if mode == "empty" {
						response.Messages = nil
					}
					if mode == "missing_entity" {
						response.Chats = nil
					}
					return encodeReadResponse(out, response)
				default:
					t.Fatalf("unexpected RPC %T", input)
					return nil
				}
			})
			for _, id := range []int64{42, 99} {
				if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, id, 12345); err != nil {
					t.Fatal(err)
				}
			}
			result, err := account.Discussion(context.Background(), post, destination)
			if mode == "valid" {
				if err != nil || result.Peer() != destination || result.TelegramID() != 100 || calls != 1 {
					t.Fatal("valid discussion failed", err)
				}
			} else if err == nil || result.String() != "" {
				t.Fatal("invalid mapping accepted", err)
			}
			if (mode == "left" || mode == "forum") && calls != 0 {
				t.Fatal("unsupported destination fetched")
			}
		})
	}
}

func TestDiscussionAndThreadMetadataNormalization(t *testing.T) {
	peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
	for _, top := range []int{0, 5, 10, -1, 11} {
		m := testMessage(20)
		m.PeerID = &tg.PeerChannel{ChannelID: 42}
		m.ReplyTo = &tg.MessageReplyHeader{ReplyToMsgID: 10, ReplyToTopID: top}
		c, err := normalizeMessage(peer, 1, m, map[int64]bool{1: true}, false)
		if err != nil {
			t.Fatal(err)
		}
		if top < 0 || top > 10 {
			if c.Message.Text != "" || c.Message.ThreadRoot != nil {
				t.Fatal("malformed thread leaked")
			}
			continue
		}
		expected := top
		if expected == 0 {
			expected = 10
		}
		if c.Message.ThreadRoot == nil || c.Message.ThreadRoot.TelegramID() != int32(expected) {
			t.Fatal("thread root lost")
		}
	}
	m := &tg.Message{ID: 20, PeerID: &tg.PeerChannel{ChannelID: 42}, Post: true, Date: 100, Message: "post"}
	m.SetReplies(tg.MessageReplies{Comments: true, ChannelID: 99, Replies: 2})
	c, err := normalizeMessage(peer, 1, m, nil, true)
	if err != nil || c.Message.DiscussionPeer != "tgpeer:v1:channel:99" || !c.Message.ValidReply() {
		t.Fatal("discussion peer lost", err)
	}
	m.Noforwards = true
	c, err = normalizeMessage(peer, 1, m, nil, true)
	if err != nil || c.Message.DiscussionPeer != "" {
		t.Fatal("protected discussion metadata leaked", err)
	}
}

func TestThreadSearchUsesBoundedExactSupergroupThread(t *testing.T) {
	for _, text := range []string{"", "needle"} {
		t.Run(text, func(t *testing.T) {
			peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
			root, _ := model.NewMessageID(peer, 5)
			calls := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch q := in.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{syntheticSupergroup()}})
				case *tg.MessagesGetRepliesRequest:
					if text != "" || q.MsgID != 5 || q.MinID != 9 || q.MaxID != 31 || q.OffsetID != 26 || q.Limit != 2 || q.Peer.(*tg.InputPeerChannel).ChannelID != 42 {
						t.Fatal("thread traversal bounds")
					}
					calls++
				case *tg.MessagesSearchRequest:
					if text == "" || q.TopMsgID != 5 || q.Q != text || q.MinID != 9 || q.MaxID != 31 || q.OffsetID != 26 || q.Limit != 2 {
						t.Fatal("thread search bounds")
					}
					calls++
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				m := testMessage(20)
				m.PeerID = &tg.PeerChannel{ChannelID: 42}
				m.ReplyTo = &tg.MessageReplyHeader{ReplyToMsgID: 10, ReplyToTopID: 5}
				return encodeReadResponse(out, &tg.MessagesChannelMessages{Pts: 12, Messages: []tg.MessageClass{m}, Chats: []tg.ChatClass{syntheticSupergroup()}, Users: []tg.UserClass{&tg.User{ID: 1}}})
			})
			if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 42, 12345); err != nil {
				t.Fatal(err)
			}
			rows, err := account.Search(context.Background(), model.SearchQuery{Peer: peer, ThreadRoot: root.String(), Query: text, MinID: 10, MaxID: 30, Before: 26, Limit: 2})
			if err != nil || len(rows) != 1 || rows[0].Message.ThreadRoot == nil || calls != 1 {
				t.Fatal("thread search failed", err)
			}
		})
	}
}

func TestBroadcastThreadSelectorNeverFetchesLinkedReplies(t *testing.T) {
	for _, query := range []string{"", "needle"} {
		t.Run(query, func(t *testing.T) {
			peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
			root, _ := model.NewMessageID(peer, 5)
			channel := syntheticSupergroup()
			channel.Megagroup = false
			channel.Broadcast = true
			calls := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch q := in.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{channel}})
				case *tg.MessagesGetHistoryRequest:
					if query != "" {
						t.Fatal("wrong traversal")
					}
					calls++
				case *tg.MessagesSearchRequest:
					if query == "" || q.TopMsgID != 0 {
						t.Fatal("broadcast thread redirected")
					}
					calls++
				default:
					t.Fatalf("unexpected cross-conversation RPC %T", in)
				}
				return encodeReadResponse(out, &tg.MessagesMessages{})
			})
			if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 42, 12345); err != nil {
				t.Fatal(err)
			}
			rows, err := account.Search(context.Background(), model.SearchQuery{Peer: peer, ThreadRoot: root.String(), Query: query, MinID: 1, MaxID: 30, Limit: 2})
			if err != nil || rows == nil || len(rows) != 0 || calls != 1 {
				t.Fatal("broadcast search failed", err)
			}
		})
	}
}

func TestDirectForumReplyRetainsTopicRoot(t *testing.T) {
	parent, _ := model.NewPeerID(model.PeerKindChannel, 42)
	peer, _ := model.NewTopicPeer(parent, 7)
	m := testMessage(20)
	m.PeerID = &tg.PeerChannel{ChannelID: 42}
	m.ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 7}
	candidate, err := normalizeMessage(peer, 1, m, map[int64]bool{1: true}, false)
	if err != nil || candidate.Message.ThreadRoot == nil || candidate.Message.ThreadRoot.Peer() != peer || candidate.Message.ThreadRoot.TelegramID() != 7 {
		t.Fatal("direct forum reply lost its root", err)
	}
}
