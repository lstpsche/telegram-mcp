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

func TestDialogPagesBoundOverfetchedPinnedResults(t *testing.T) {
	calls := 0
	account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.UpdatesGetStateRequest); ok {
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		}
		q, ok := in.(*tg.MessagesGetDialogsRequest)
		if !ok || q.Limit != 1 {
			return errors.New("unexpected RPC")
		}
		calls++
		ids := []int64{1, 2, 3, 4, 5}
		if calls <= 4 {
			if q.ExcludePinned || q.OffsetID != 0 {
				t.Fatal("pinned continuation must start from the live pinned list")
			}
		} else {
			if !q.ExcludePinned || q.OffsetID != 4 {
				t.Fatal("ordinary continuation must exclude already delivered pins")
			}
			ids = []int64{5}
		}
		page := &tg.MessagesDialogs{}
		for _, id := range ids {
			top := int(id)
			date := 100 - int(id)
			// New messages do not change pin order.
			if id <= 3 {
				top += calls * 10
				date = 100 + top
			}
			page.Dialogs = append(page.Dialogs, &tg.Dialog{Pinned: id <= 3, Peer: &tg.PeerChat{ChatID: id}, TopMessage: top})
			page.Chats = append(page.Chats, &tg.Chat{ID: id, Title: "synthetic", Photo: &tg.ChatPhotoEmpty{}, Date: 100})
			page.Messages = append(page.Messages, &tg.Message{ID: top, PeerID: &tg.PeerChat{ChatID: id}, Date: date, Message: "incidental body"})
		}
		return encodeReadResponse(out, page)
	})
	position := model.DialogPosition{}
	for id := 1; id <= 5; id++ {
		page, err := account.Dialogs(context.Background(), position, 1)
		if err != nil {
			t.Fatal("overfetched page failed", err)
		}
		if page.Scanned != 1 || len(page.Items) != 1 || page.Items[0].Chat.ID.String() != fmt.Sprintf("tgpeer:v1:chat:%d", id) || page.Next == nil {
			t.Fatal("page skipped or repeated a dialog", page)
		}
		position = *page.Next
	}
	if position != (model.DialogPosition{Folder: 1}) || calls != 5 {
		t.Fatal("folder ended before buffered dialogs were consumed")
	}
}

func TestDialogPagesTraverseExcludedBoundaryAndArchivedFolder(t *testing.T) {
	calls := 0
	account, db := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch q := in.(type) {
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		case *tg.MessagesGetDialogsRequest:
			calls++
			if q.Limit != 1 {
				t.Fatal("candidate limit changed")
			}
			switch calls {
			case 1:
				group := &tg.Channel{ID: 8, Broadcast: true, Left: true, Title: "excluded title", Photo: &tg.ChatPhotoEmpty{}, Date: 100}
				group.SetAccessHash(789)
				return encodeReadResponse(out, &tg.MessagesDialogsSlice{Count: 2, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 8}, TopMessage: 4}}, Chats: []tg.ChatClass{group}, Messages: []tg.MessageClass{&tg.Message{ID: 4, PeerID: &tg.PeerChannel{ChannelID: 8}, Date: 90, Message: "incidental secret body"}}})
			case 2:
				input, ok := q.OffsetPeer.(*tg.InputPeerChannel)
				if !ok || input.ChannelID != 8 || input.AccessHash != 789 || q.OffsetID != 4 || q.OffsetDate != 90 {
					t.Fatal("offset triple/hash not preserved")
				}
				group := syntheticSupergroup()
				group.Megagroup, group.Broadcast = false, true
				return encodeReadResponse(out, &tg.MessagesDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 42}, TopMessage: 7, UnreadCount: 3}}, Chats: []tg.ChatClass{group}})
			case 3:
				folder, ok := q.GetFolderID()
				if !ok || folder != 1 || q.OffsetID != 0 {
					t.Fatal("archive traversal missing")
				}
				return encodeReadResponse(out, &tg.MessagesDialogs{})
			}
		}
		return errors.New("unexpected RPC")
	})
	first, err := account.Dialogs(context.Background(), model.DialogPosition{}, 1)
	if err != nil || len(first.Items) != 0 || first.Next == nil || first.Scanned != 1 {
		t.Fatal("excluded page", first, err)
	}
	second, err := account.Dialogs(context.Background(), *first.Next, 1)
	if err != nil || len(second.Items) != 1 || !second.Items[0].Chat.Broadcast || second.Items[0].Unread.Count != 3 || second.Next == nil || second.Next.Folder != 1 {
		t.Fatal("broadcast page", second, err)
	}
	last, err := account.Dialogs(context.Background(), *second.Next, 1)
	if err != nil || last.Next != nil || len(last.Items) != 0 || calls != 3 {
		t.Fatal("archive end", last, err)
	}
	var checkpoints int
	if err := db.QueryRow("SELECT count(*) FROM telegram_channel_state").Scan(&checkpoints); err != nil || checkpoints != 0 {
		t.Fatal("navigation created channel checkpoints", err)
	}
}

func TestDialogPagesRejectMalformedOrNonprogressingResults(t *testing.T) {
	for _, failure := range []string{"oversized", "messages_oversized", "missing_date", "duplicate", "wrong_message_peer", "not_modified", "repeat", "bad_count", "missing_pin", "unpinned_boundary", "misordered_pin", "unexpected_pin"} {
		t.Run(failure, func(t *testing.T) {
			account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.MessagesGetDialogsRequest); !ok {
					return errors.New("unexpected RPC")
				}
				if failure == "not_modified" {
					return encodeReadResponse(out, &tg.MessagesDialogsNotModified{})
				}
				page := &tg.MessagesDialogsSlice{Count: 2, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChat{ChatID: 42}, TopMessage: 7}}, Chats: []tg.ChatClass{&tg.Chat{ID: 42, Title: "synthetic", Photo: &tg.ChatPhotoEmpty{}, Date: 100}}, Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerChat{ChatID: 42}, Date: 90}}}
				switch failure {
				case "oversized":
					for len(page.Dialogs) < 201 {
						page.Dialogs = append(page.Dialogs, page.Dialogs[0])
					}
					page.Count = 201
				case "messages_oversized":
					for len(page.Messages) < 201 {
						page.Messages = append(page.Messages, page.Messages[0])
					}
				case "missing_date":
					page.Messages = nil
				case "duplicate":
					page.Dialogs = append(page.Dialogs, page.Dialogs[0])
				case "wrong_message_peer":
					page.Messages[0].(*tg.Message).PeerID = &tg.PeerChat{ChatID: 43}
				case "bad_count":
					page.Count = 0
				case "unexpected_pin":
					page.Dialogs[0].(*tg.Dialog).Pinned = true
				case "misordered_pin":
					page.Dialogs = append(page.Dialogs, &tg.Dialog{Pinned: true, Peer: &tg.PeerChat{ChatID: 43}, TopMessage: 8})
				}
				return encodeReadResponse(out, page)
			})
			position := model.DialogPosition{}
			limit := 1
			if failure == "repeat" {
				position = model.DialogPosition{Peer: "tgpeer:v1:chat:42", MessageID: 7, Date: 90}
			}
			if failure == "missing_pin" {
				position = model.DialogPosition{Peer: "tgpeer:v1:chat:43", MessageID: 7, Date: 90, Pinned: true}
			}
			if failure == "unpinned_boundary" {
				position = model.DialogPosition{Peer: "tgpeer:v1:chat:42", MessageID: 6, Date: 90, Pinned: true}
			}
			if failure == "unexpected_pin" {
				position = model.DialogPosition{Peer: "tgpeer:v1:chat:43", MessageID: 6, Date: 90}
			}
			if failure == "duplicate" {
				limit = 2
			}
			page, err := account.Dialogs(context.Background(), position, limit)
			if err == nil || len(page.Items) != 0 || page.Next != nil {
				t.Fatal("malformed page returned", page, err)
			}
			expected := model.ErrorInvalidReference
			if failure == "oversized" || failure == "messages_oversized" {
				expected = model.ErrorResultTooLarge
			}
			if failure == "missing_pin" || failure == "unpinned_boundary" {
				expected = model.ErrorCursorInvalid
			}
			if model.TextErrorCategory(err) != expected {
				t.Fatal("wrong failure boundary", err)
			}
		})
	}
}
