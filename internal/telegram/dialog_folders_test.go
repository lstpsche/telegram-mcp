package telegram

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestDialogPagesExcludePinsFromOtherFolders(t *testing.T) {
	for _, folder := range []int{0, 1} {
		t.Run(fmt.Sprint(folder), func(t *testing.T) {
			calls := 0
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.UpdatesGetStateRequest); ok {
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				}
				q, ok := in.(*tg.MessagesGetDialogsRequest)
				if !ok {
					t.Fatalf("unexpected RPC %T", in)
				}
				requested, set := q.GetFolderID()
				if !set || requested != folder || q.Limit != 1 {
					t.Fatal("folder request changed")
				}
				calls++
				ids := []int64{1, 2, 3, 4, 5}
				if calls == 4 {
					if !q.ExcludePinned || q.OffsetID != 4 {
						t.Fatal("ordinary offset changed")
					}
					ids = []int64{5}
				} else if q.ExcludePinned || q.OffsetID != 0 {
					t.Fatal("pinned prefix not retained")
				}
				page := &tg.MessagesDialogs{}
				for _, id := range ids {
					dialog := &tg.Dialog{Peer: &tg.PeerChat{ChatID: id}, Pinned: id <= 3, TopMessage: int(id)}
					assigned := folder
					if id == 1 {
						assigned = 1 - folder
					}
					if assigned != 0 {
						dialog.SetFolderID(assigned)
					}
					page.Dialogs = append(page.Dialogs, dialog)
					page.Chats = append(page.Chats, &tg.Chat{ID: id, Photo: &tg.ChatPhotoEmpty{}})
					page.Messages = append(page.Messages, &tg.Message{ID: int(id), PeerID: &tg.PeerChat{ChatID: id}, Date: 100 - int(id)})
				}
				return encodeReadResponse(out, page)
			})
			position := model.DialogPosition{Folder: folder}
			for id := 2; id <= 5; id++ {
				page, err := account.Dialogs(context.Background(), position, 1)
				if err != nil {
					t.Fatal("folder page failed", err)
				}
				if page.Scanned != 1 || len(page.Items) != 1 || page.Items[0].Chat.ID.String() != fmt.Sprintf("tgpeer:v1:chat:%d", id) {
					t.Fatal("wrong folder dialog delivered")
				}
				if id < 5 {
					if page.Next == nil {
						t.Fatal("folder ended early")
					}
					position = *page.Next
				} else if folder == 0 {
					if page.Next == nil || *page.Next != (model.DialogPosition{Folder: 1}) {
						t.Fatal("archive transition missing")
					}
				} else if page.Next != nil {
					t.Fatal("archive did not end")
				}
			}
			if calls != 4 {
				t.Fatal("unexpected lookup count")
			}
		})
	}
}

func TestDialogPageWithOnlyForeignPinsEndsArchive(t *testing.T) {
	for _, sliced := range []bool{false, true} {
		t.Run(fmt.Sprint(sliced), func(t *testing.T) {
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesGetDialogsRequest:
					dialogs := []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, Pinned: true, TopMessage: 10}}
					if sliced {
						return encodeReadResponse(out, &tg.MessagesDialogsSlice{Count: 5, Dialogs: dialogs})
					}
					return encodeReadResponse(out, &tg.MessagesDialogs{Dialogs: dialogs})
				default:
					t.Fatalf("unexpected RPC %T", in)
					return nil
				}
			})
			page, err := account.Dialogs(context.Background(), model.DialogPosition{Folder: 1}, 1)
			if sliced {
				if model.TextErrorCategory(err) != model.ErrorInvalidReference {
					t.Fatal("unresumable slice became success", err)
				}
			} else if err != nil || len(page.Items) != 0 || page.Scanned != 0 || page.Next != nil {
				t.Fatal("foreign pins became archived chats", err)
			}
		})
	}
}

func TestDialogPagesRejectInvalidFolderBoundaries(t *testing.T) {
	for _, mode := range []string{"negative", "unknown", "ordinary_mismatch", "moved_pin"} {
		t.Run(mode, func(t *testing.T) {
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.UpdatesGetStateRequest:
					return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
				case *tg.MessagesGetDialogsRequest:
					dialog := &tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, TopMessage: 10, Pinned: mode != "ordinary_mismatch"}
					if mode == "negative" {
						dialog.SetFolderID(-1)
					}
					if mode == "unknown" {
						dialog.SetFolderID(2)
					}
					return encodeReadResponse(out, &tg.MessagesDialogs{Dialogs: []tg.DialogClass{dialog}})
				default:
					t.Fatalf("unexpected RPC %T", in)
					return nil
				}
			})
			position := model.DialogPosition{Folder: 1}
			expected := model.ErrorInvalidReference
			if mode == "moved_pin" {
				position.Peer = "tgpeer:v1:self:1"
				position.MessageID = 10
				position.Date = 100
				position.Pinned = true
				expected = model.ErrorCursorInvalid
			}
			_, err := account.Dialogs(context.Background(), position, 1)
			if model.TextErrorCategory(err) != expected {
				t.Fatal("invalid folder boundary accepted", err)
			}
		})
	}
}
