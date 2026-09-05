package telegram

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestEmptySearchRevalidatesPeerWithoutRequiringResultEntities(t *testing.T) {
	for _, kind := range []model.PeerKind{model.PeerKindUser, model.PeerKindChat, model.PeerKindChannel} {
		for _, sliced := range []bool{false, true} {
			for _, mode := range []string{"valid", "changed", "recheck_error", "upstream", "checkpoint", "nonempty_missing_entity"} {
				t.Run(fmt.Sprintf("%s/sliced=%t/%s", kind, sliced, mode), func(t *testing.T) {
					searched := false
					checks := 0
					account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
						switch in.(type) {
						case *tg.UpdatesGetStateRequest:
							if searched && mode == "checkpoint" {
								return errors.New("synthetic checkpoint failure")
							}
							return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
						case *tg.MessagesSearchRequest:
							searched = true
							if mode == "upstream" {
								return errors.New("synthetic search failure")
							}
							page := &tg.MessagesMessages{}
							if mode == "nonempty_missing_entity" {
								page.Messages = []tg.MessageClass{testMessage(20)}
							}
							if sliced {
								return encodeReadResponse(out, &tg.MessagesMessagesSlice{Count: 10, Messages: page.Messages})
							}
							return encodeReadResponse(out, page)
						case *tg.UsersGetUsersRequest, *tg.MessagesGetChatsRequest, *tg.ChannelsGetChannelsRequest:
							checks++
							if searched && mode == "recheck_error" {
								return errors.New("synthetic peer lookup failure")
							}
							changed := searched && mode == "changed"
							switch kind {
							case model.PeerKindUser:
								return encodeReadResponse(out, &tg.UserClassVector{Elems: []tg.UserClass{&tg.User{ID: 42, Bot: changed}}})
							case model.PeerKindChat:
								return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{&tg.Chat{ID: 42, Noforwards: changed, Photo: &tg.ChatPhotoEmpty{}}}})
							default:
								group := syntheticSupergroup()
								group.Broadcast = changed
								return encodeReadResponse(out, &tg.MessagesChats{Chats: []tg.ChatClass{group}})
							}
						default:
							t.Fatalf("unexpected RPC %T", in)
							return nil
						}
					})
					ctx := context.Background()
					if err := account.reads.storage.SetUserAccessHash(ctx, 1, 42, 12345); err != nil {
						t.Fatal(err)
					}
					if err := account.reads.storage.SetChannelAccessHash(ctx, 1, 42, 12345); err != nil {
						t.Fatal(err)
					}
					peer, _ := model.NewPeerID(kind, 42)
					rows, err := account.Search(ctx, model.SearchQuery{Peer: peer, Query: "synthetic", MinID: 1, MaxID: 100, Limit: 5})
					if mode == "valid" {
						if err != nil || rows == nil || len(rows) != 0 || checks != 2 {
							t.Fatal("legitimate empty result rejected", err, checks)
						}
					} else if err == nil || rows != nil {
						t.Fatal("failed search became empty success", mode, err)
					}
				})
			}
		}
	}
}

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

func TestDateSearchUsesExclusiveRPCBoundsAndValidatesDelivery(t *testing.T) {
	for _, date := range []int{99, 100, 101, 102} {
		t.Run(fmt.Sprint(date), func(t *testing.T) {
			searched := false
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch q := in.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesSearchRequest:
					searched = true
					if q.Q != "" || q.MinDate != 99 || q.MaxDate != 102 || q.OffsetID != 21 || q.Limit != 2 {
						t.Fatal("incorrect date search request")
					}
					message := testMessage(20)
					message.Date = date
					return encodeReadResponse(out, &tg.MessagesMessages{Messages: []tg.MessageClass{message}})
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			})
			rows, err := account.Search(context.Background(), model.SearchQuery{Peer: testSelfPeer(t), Window: &model.DateWindow{Since: 100, Until: 102}, MinID: 10, MaxID: 20, Limit: 2})
			if !searched {
				t.Fatal("search not invoked")
			}
			if date >= 100 && date < 102 {
				if err != nil || len(rows) != 1 {
					t.Fatal("valid date rejected", err)
				}
			} else if model.TextErrorCategory(err) != model.ErrorInvalidReference || rows != nil {
				t.Fatal("out-of-window result released")
			}
		})
	}
}

func TestDateSearchRejectsUnboundedOrMixedQueryBeforeIO(t *testing.T) {
	account, _ := newReadTestAccount(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("invalid input reached Telegram")
		return nil
	})
	for _, q := range []model.SearchQuery{
		{Peer: testSelfPeer(t), MinID: 1, MaxID: 20, Limit: 2},
		{Peer: testSelfPeer(t), Query: "mixed", Window: &model.DateWindow{Since: 100, Until: 102}, MinID: 1, MaxID: 20, Limit: 2},
		{Peer: testSelfPeer(t), Window: &model.DateWindow{Since: 102, Until: 100}, MinID: 1, MaxID: 20, Limit: 2},
	} {
		if _, err := account.Search(context.Background(), q); model.TextErrorCategory(err) != model.ErrorInvalidInput {
			t.Fatal("invalid date query accepted")
		}
	}
}
