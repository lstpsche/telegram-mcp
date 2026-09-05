package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func syntheticSupergroup() *tg.Channel {
	g := &tg.Channel{ID: 42, Megagroup: true, Title: "Synthetic group", Photo: &tg.ChatPhotoEmpty{}, Date: 100}
	g.SetAccessHash(12345)
	return g
}

func TestSupergroupLiveHistorySearchUnreadAndReceipt(t *testing.T) {
	group := syntheticSupergroup()
	peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
	var history, search, receipts, readbacks int
	read := false
	account, _ := newReadTestAccount(t, func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch q := in.(type) {
		case *tg.ChannelsGetChannelsRequest:
			return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{group}})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesGetHistoryRequest:
			history++
			p, ok := q.Peer.(*tg.InputPeerChannel)
			if !ok || p.ChannelID != 42 || q.MinID != 0 || q.MaxID != 0 {
				t.Fatal("wrong channel history routing")
			}
			return encodeReadResponse(out, &tg.MessagesChannelMessages{Pts: 12, Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 42}, FromID: &tg.PeerUser{UserID: 2}, Date: 100, Message: "synthetic group text"}}, Users: []tg.UserClass{&tg.User{ID: 2}}, Chats: []tg.ChatClass{group}})
		case *tg.MessagesSearchRequest:
			search++
			if _, ok := q.Peer.(*tg.InputPeerChannel); !ok {
				t.Fatal("search not peer scoped")
			}
			return encodeReadResponse(out, &tg.MessagesChannelMessages{Pts: 12, Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 42}, FromID: &tg.PeerUser{UserID: 2}, Date: 100, Message: "synthetic group text"}}, Users: []tg.UserClass{&tg.User{ID: 2}}, Chats: []tg.ChatClass{group}})
		case *tg.ChannelsReadHistoryRequest:
			receipts++
			if q.MaxID != 7 {
				t.Fatal("wrong read ceiling")
			}
			read = true
			return encodeReadResponse(out, &tg.BoolTrue{})
		case *tg.MessagesGetPeerDialogsRequest:
			readbacks++
			max := 0
			if read {
				max = 7
			}
			return encodeReadResponse(out, &tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 42}, ReadInboxMaxID: max, UnreadCount: 2}}, Chats: []tg.ChatClass{group}, State: tg.UpdatesState{Pts: 10, Date: 100, Seq: 1}})
		default:
			return errors.New("unexpected RPC")
		}
	})
	if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 42, 12345); err != nil {
		t.Fatal(err)
	}
	messages, err := account.History(context.Background(), model.HistoryQuery{Peer: peer, MinID: 1, MaxID: 2147483647, Limit: 20})
	if err != nil || len(messages) != 1 || messages[0].Message.Text != "synthetic group text" {
		t.Fatal("channel history", err)
	}
	if _, err := account.Search(context.Background(), model.SearchQuery{Peer: peer, Query: "synthetic", MinID: 1, MaxID: 2147483647, Limit: 20}); err != nil {
		t.Fatal(err)
	}
	unread, err := account.Unread(context.Background(), peer)
	if err != nil || unread.Count != 2 {
		t.Fatal(unread, err)
	}
	if err := account.Acknowledge(context.Background(), peer, 7); err != nil {
		t.Fatal(err)
	}
	if history != 1 || search != 1 || receipts != 1 || readbacks != 2 {
		t.Fatal("missing live operation", history, search, receipts, readbacks)
	}
}

func TestSupergroupRejectsUnsupportedMetadataBeforeHistory(t *testing.T) {
	for _, kind := range []string{"broadcast", "forum", "left", "protected", "min", "gigagroup", "monoforum", "restricted", "forbidden", "wrong_id"} {
		t.Run(kind, func(t *testing.T) {
			group := syntheticSupergroup()
			var value tg.ChatClass = group
			switch kind {
			case "broadcast":
				group.Broadcast = true
			case "forum":
				group.Forum = true
			case "left":
				group.Left = true
			case "protected":
				group.Noforwards = true
			case "min":
				group.Min = true
			case "gigagroup":
				group.Gigagroup = true
			case "monoforum":
				group.Monoforum = true
			case "restricted":
				group.Restricted = true
			case "forbidden":
				value = &tg.ChannelForbidden{ID: 42, AccessHash: 12345}
			case "wrong_id":
				group.ID = 43
			}
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.ChannelsGetChannelsRequest); !ok {
					t.Fatal("unsupported group fetched content")
				}
				return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{value}})
			})
			if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 42, 12345); err != nil {
				t.Fatal(err)
			}
			peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
			if _, err := account.History(context.Background(), model.HistoryQuery{Peer: peer, MinID: 1, MaxID: 100, Limit: 20}); model.TextErrorCategory(err) != model.ErrorUnsupportedPeer {
				t.Fatal(err)
			}
		})
	}
}

func TestSupergroupReceiptRequiresPositiveExactReadback(t *testing.T) {
	for _, failure := range []string{"false", "stale", "wrong_peer", "protected", "bad_state", "rpc"} {
		t.Run(failure, func(t *testing.T) {
			group := syntheticSupergroup()
			receipted := false
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{group}})
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.ChannelsReadHistoryRequest:
					receipted = true
					if failure == "false" {
						return encodeReadResponse(out, &tg.BoolFalse{})
					}
					if failure == "rpc" {
						return errors.New("synthetic receipt failure")
					}
					return encodeReadResponse(out, &tg.BoolTrue{})
				case *tg.MessagesGetPeerDialogsRequest:
					if !receipted {
						t.Fatal("readback preceded receipt")
					}
					dialog := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 42}, ReadInboxMaxID: 7}
					state := tg.UpdatesState{Pts: 10, Date: 100, Seq: 1}
					if failure == "stale" {
						dialog.ReadInboxMaxID = 6
					}
					if failure == "wrong_peer" {
						dialog.Peer = &tg.PeerChannel{ChannelID: 43}
					}
					if failure == "protected" {
						group.Noforwards = true
					}
					if failure == "bad_state" {
						state.Date = 0
					}
					return encodeReadResponse(out, &tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{dialog}, Chats: []tg.ChatClass{group}, State: state})
				default:
					return errors.New("unexpected RPC")
				}
			})
			if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 42, 12345); err != nil {
				t.Fatal(err)
			}
			peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
			if err := account.Acknowledge(context.Background(), peer, 7); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}

func TestSupergroupRevalidatesHistoryEntityAndExactImage(t *testing.T) {
	for _, change := range []string{"none", "protected", "forum", "missing", "wrong_constructor", "zero_pts"} {
		t.Run(change, func(t *testing.T) {
			peer, _ := model.NewPeerID(model.PeerKindChannel, 42)
			message := testPhotoMessage()
			message.PeerID = &tg.PeerChannel{ChannelID: 42}
			message.FromID = &tg.PeerUser{UserID: 2}
			expected, err := normalizeMessage(peer, 1, message, map[int64]bool{2: true})
			if err != nil || expected.Image == nil {
				t.Fatal("invalid image fixture", err)
			}
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.ChannelsGetChannelsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{syntheticSupergroup()}})
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesGetHistoryRequest:
					group := syntheticSupergroup()
					page := &tg.MessagesChannelMessages{Pts: 12, Messages: []tg.MessageClass{message}, Users: []tg.UserClass{&tg.User{ID: 2}}, Chats: []tg.ChatClass{group}}
					switch change {
					case "protected":
						group.Noforwards = true
					case "forum":
						group.Forum = true
					case "missing":
						page.Chats = nil
					case "zero_pts":
						page.Pts = 0
					case "wrong_constructor":
						return encodeReadResponse(out, &tg.MessagesMessages{Messages: page.Messages, Users: page.Users, Chats: page.Chats})
					}
					return encodeReadResponse(out, page)
				default:
					return errors.New("unexpected RPC")
				}
			})
			if err := account.reads.storage.SetChannelAccessHash(context.Background(), 1, 42, 12345); err != nil {
				t.Fatal(err)
			}
			location, err := account.exactImage(context.Background(), expected)
			if change == "none" {
				if err != nil || location == nil || location.source != *expected.Image {
					t.Fatal("exact supergroup image rejected", err)
				}
			} else if err == nil || location != nil {
				t.Fatal("changed entity released image")
			}
		})
	}
}
