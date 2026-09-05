package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestSearchUsesExactPeerAndGrantedOffset(t *testing.T) {
	calls := 0
	account, _ := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
		switch q := input.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesSearchRequest:
			calls++
			if _, ok := q.Peer.(*tg.InputPeerSelf); !ok {
				t.Fatal("unscoped search")
			}
			if q.Q != "needle" || q.OffsetID != 21 || q.MinID != 9 || q.MaxID != 21 || q.Limit != 2 || q.Hash != 0 {
				t.Fatal("search bounds incorrect")
			}
			return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{testMessage(20), testMessage(18)}})
		default:
			t.Errorf("unexpected RPC %T", input)
			return errors.New("unexpected RPC")
		}
	})
	rows, err := account.Search(context.Background(), model.SearchQuery{Peer: testSelfPeer(t), Query: "needle", MinID: 10, MaxID: 20, Limit: 2})
	if err != nil || len(rows) != 2 || calls != 1 {
		t.Fatal("scoped search failed", err)
	}
}

func TestSearchRejectsUnsupportedOrMalformedResults(t *testing.T) {
	for _, failure := range []string{"channel", "crosspeer", "range", "notmodified", "upstream"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesSearchRequest:
					calls++
					if failure == "upstream" {
						return errors.New("private query error")
					}
					if failure == "notmodified" {
						return encodeReadResponse(out, &tg.MessagesMessagesNotModified{})
					}
					message := testMessage(20)
					if failure == "crosspeer" {
						message.PeerID = &tg.PeerUser{UserID: 99}
					}
					if failure == "range" {
						message.ID = 25
					}
					return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{message}})
				default:
					return errors.New("unexpected RPC")
				}
			})
			peer := testSelfPeer(t)
			if failure == "channel" {
				peer, _ = model.NewPeerID(model.PeerKindChannel, 1)
			}
			rows, err := account.Search(context.Background(), model.SearchQuery{Peer: peer, Query: "q", MinID: 10, MaxID: 20, Limit: 2})
			if err == nil || rows != nil {
				t.Fatal("unsafe search released candidates")
			}
			if failure == "channel" && calls != 0 {
				t.Fatal("unsupported peer fetched")
			}
		})
	}
}

func TestUnreadDiscardsIncidentalBodiesAndRejectsInvalidMetadata(t *testing.T) {
	for _, mode := range []string{"valid", "negative", "crosspeer", "invalidstate", "missing"} {
		t.Run(mode, func(t *testing.T) {
			account, _ := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch q := input.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesGetPeerDialogsRequest:
					if len(q.Peers) != 1 {
						t.Fatal("unread requested multiple peers")
					}
					input, ok := q.Peers[0].(*tg.InputDialogPeer)
					if !ok {
						t.Fatal("wrong dialog constructor")
					}
					if _, ok := input.Peer.(*tg.InputPeerSelf); !ok {
						t.Fatal("unscoped unread")
					}
					dialog := &tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, UnreadCount: 4, UnreadMark: true}
					response := &tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{dialog}, Messages: []tg.MessageClass{testMessage(20)}, State: tg.UpdatesState{Pts: 10, Date: 100, Seq: 1}}
					response.Messages[0].(*tg.Message).Message = "incidental private body"
					switch mode {
					case "negative":
						dialog.UnreadCount = -1
					case "crosspeer":
						dialog.Peer = &tg.PeerUser{UserID: 99}
					case "invalidstate":
						response.State.Seq = -1
					case "missing":
						response.Dialogs = nil
					}
					return encodeReadResponse(out, response)
				default:
					t.Errorf("unexpected RPC %T", input)
					return errors.New("unexpected RPC")
				}
			})
			unread, err := account.Unread(context.Background(), testSelfPeer(t))
			if mode == "valid" {
				if err != nil || unread.Count != 4 || !unread.Marked {
					t.Fatal("unread failed", err)
				}
			} else if err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
}

func TestUnreadRevalidatesGroupInDialogResponse(t *testing.T) {
	for _, mode := range []string{"ordinary", "forbidden", "protected", "missing"} {
		t.Run(mode, func(t *testing.T) {
			account, _ := newReadTestAccount(t, func(_ context.Context, input bin.Encoder, out bin.Decoder) error {
				switch input.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesGetChatsRequest:
					return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{&tg.Chat{ID: 42, Title: "ordinary", Photo: &tg.ChatPhotoEmpty{}}}})
				case *tg.MessagesGetPeerDialogsRequest:
					chat := &tg.Chat{ID: 42, Title: "ordinary", Photo: &tg.ChatPhotoEmpty{}}
					response := &tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChat{ChatID: 42}, UnreadCount: 2}}, Chats: []tg.ChatClass{chat}, State: tg.UpdatesState{Pts: 10, Date: 100, Seq: 1}}
					switch mode {
					case "forbidden":
						response.Chats = []tg.ChatClass{&tg.ChatForbidden{ID: 42}}
					case "protected":
						chat.Noforwards = true
					case "missing":
						response.Chats = nil
					}
					return encodeReadResponse(out, response)
				default:
					return errors.New("unexpected RPC")
				}
			})
			peer, _ := model.NewPeerID(model.PeerKindChat, 42)
			unread, err := account.Unread(context.Background(), peer)
			if mode == "ordinary" {
				if err != nil || unread.Count != 2 {
					t.Fatal("ordinary group rejected", err)
				}
			} else if model.TextErrorCategory(err) != model.ErrorUnsupportedPeer || unread != (model.Unread{}) {
				t.Fatal("changed group released unread metadata", err)
			}
		})
	}
}

func TestMessagePageRevalidatesReturnedPeerConstructors(t *testing.T) {
	for _, kind := range []model.PeerKind{model.PeerKindUser, model.PeerKindChat} {
		for _, mode := range []string{"ordinary", "missing", "empty", "changed"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
					t.Fatal("unexpected RPC")
					return nil
				})
				peer, _ := model.NewPeerID(kind, 2)
				message := testMessage(20)
				page := &tg.MessagesMessages{Messages: []tg.MessageClass{message}}
				if kind == model.PeerKindUser {
					message.PeerID = &tg.PeerUser{UserID: 2}
					page.Users = []tg.UserClass{&tg.User{ID: 2}}
					switch mode {
					case "missing":
						page.Users = nil
					case "empty":
						page.Users = []tg.UserClass{&tg.UserEmpty{ID: 2}}
					case "changed":
						page.Users[0].(*tg.User).Bot = true
					}
				} else {
					message.PeerID = &tg.PeerChat{ChatID: 2}
					page.Chats = []tg.ChatClass{&tg.Chat{ID: 2}}
					switch mode {
					case "missing":
						page.Chats = nil
					case "empty":
						page.Chats = []tg.ChatClass{&tg.ChatForbidden{ID: 2}}
					case "changed":
						page.Chats[0].(*tg.Chat).SetMigratedTo(&tg.InputChannel{ChannelID: 2, AccessHash: 1})
					}
				}
				rows, err := account.reads.normalizePage(context.Background(), peer, page, 1)
				if mode == "ordinary" {
					if err != nil || len(rows) != 1 || rows[0].Message.Text == "" {
						t.Fatal("ordinary peer rejected", err)
					}
				} else if model.TextErrorCategory(err) != model.ErrorUnsupportedPeer || rows != nil {
					t.Fatal("unsafe peer released message candidates", err)
				}
			})
		}
	}
}
