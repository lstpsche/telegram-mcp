package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestFolderRulesRespectExplicitPeersAndUnreadMentions(t *testing.T) {
	peer, _ := model.NewPeerID(model.PeerKindUser, 2)
	user := &tg.User{ID: 2, Contact: true}
	rules := &tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: "synthetic"}, Contacts: true, ExcludeRead: true, ExcludeMuted: true, ExcludeArchived: true}
	f, err := normalizeFolder(rules, 1)
	if err != nil {
		t.Fatal(err)
	}
	f.muted[0] = true
	d := &tg.Dialog{UnreadCount: 1}
	check := func(want bool) {
		t.Helper()
		got, err := f.matches(peer, model.Chat{ID: peer}, d, []tg.UserClass{user})
		if err != nil || got != want {
			t.Fatalf("match=%v error=%v want=%v", got, err, want)
		}
	}
	check(false)
	d.UnreadMentionsCount = 1
	check(true)
	d.SetFolderID(1)
	check(false)
	f.included[peer] = true
	f.excluded[peer] = true
	check(true)
	delete(f.included, peer)
	check(false)
	delete(f.excluded, peer)
	d.SetFolderID(0)
	d.UnreadMentionsCount = 0
	d.NotifySettings.SetMuteUntil(0)
	check(true)
	d.UnreadCount = 0
	check(false)
	d.UnreadMark = true
	check(true)
	user.Bot = true
	check(false)
	f.rules.Bots = true
	check(true)
	user.Bot = false
	user.Contact = false
	check(false)
	f.rules.NonContacts = true
	check(true)
}

func TestFolderSnapshotTraversesArchiveWithoutBodiesOrHashes(t *testing.T) {
	calls := 0
	account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch q := in.(type) {
		case *tg.MessagesGetDialogFiltersRequest:
			return encodeReadResponse(out, &tg.MessagesDialogFilters{Filters: []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: "private folder title"}, Groups: true, ExcludeArchived: true, IncludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 2}}}}})
		case *tg.MessagesGetDialogsRequest:
			calls++
			folder, _ := q.GetFolderID()
			id := int64(folder + 1)
			d := &tg.Dialog{Peer: &tg.PeerChat{ChatID: id}, TopMessage: 5}
			d.SetFolderID(folder)
			return encodeReadResponse(out, &tg.MessagesDialogs{Dialogs: []tg.DialogClass{d}, Chats: []tg.ChatClass{&tg.Chat{ID: id, Title: "private chat title", Photo: &tg.ChatPhotoEmpty{}, Date: 100}}, Messages: []tg.MessageClass{&tg.Message{ID: 5, PeerID: d.Peer, Message: "private body", Date: 100}}})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		default:
			return errors.New("unexpected RPC or receipt")
		}
	})
	result, err := account.folders(context.Background(), 2)
	if err != nil || len(result) != 1 || len(result[0].Peers) != 2 || calls != 2 {
		t.Fatalf("snapshot counts or error: %d %d %v", len(result), calls, err)
	}
}

func TestFolderRejectsUnknownOrUnavailableExplicitMembership(t *testing.T) {
	account, _ := newReadTestAccount(t, func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetDialogFiltersRequest:
			return encodeReadResponse(out, &tg.MessagesDialogFilters{Filters: []tg.DialogFilterClass{&tg.DialogFilterChatlist{ID: 2, Title: tg.TextWithEntities{Text: "synthetic"}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 99}}}}})
		case *tg.MessagesGetDialogsRequest:
			return encodeReadResponse(out, &tg.MessagesDialogs{})
		case *tg.UpdatesGetStateRequest:
			return encodeReadResponse(out, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
		default:
			return errors.New("unexpected RPC")
		}
	})
	for _, id := range []int32{2, 3} {
		result, err := account.folders(context.Background(), id)
		if err == nil || result != nil {
			t.Fatal("unavailable membership returned success")
		}
	}
}
