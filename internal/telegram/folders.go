package telegram

import (
	"context"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

type folderFilter struct {
	folder   model.Folder
	rules    tg.DialogFilter
	included map[model.PeerID]bool
	excluded map[model.PeerID]bool
	muted    [3]bool // users, groups, broadcasts
	now      int64
}

// DiscoverFolders is human-only: zero lists folder titles; an exact folder ID
// resolves a bounded, complete snapshot of its supported joined dialogs.
func (a *Account) DiscoverFolders(ctx context.Context, id int32) ([]model.Folder, error) {
	if id != 0 && id < 2 {
		return nil, model.TextError(model.ErrorInvalidInput, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var result []model.Folder
	err := a.Observe(bounded, func(ctx context.Context, status AuthorizationStatus) error {
		if !status.Authorized || !a.Ready() {
			return model.TextError(model.ErrorNotReady, nil)
		}
		var err error
		result, err = a.folders(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (a *Account) folders(ctx context.Context, id int32) ([]model.Folder, error) {
	response, err := a.reads.api.MessagesGetDialogFilters(ctx)
	if err != nil {
		return nil, readError(err)
	}
	if response == nil || len(response.Filters) > 100 {
		return nil, model.TextError(model.ErrorResultTooLarge, nil)
	}
	result := make([]model.Folder, 0, len(response.Filters))
	seen := make(map[int32]bool)
	var selected *folderFilter
	for _, value := range response.Filters {
		if _, ok := value.(*tg.DialogFilterDefault); ok {
			continue
		}
		filter, err := normalizeFolder(value, a.reads.self.Load())
		if err != nil {
			return nil, err
		}
		if seen[filter.folder.ID] {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		seen[filter.folder.ID] = true
		result = append(result, filter.folder)
		if filter.folder.ID == id {
			selected = filter
		}
	}
	if id == 0 {
		if err := a.reads.synchronize(ctx); err != nil {
			return nil, model.TextError(model.ErrorFreshnessDegraded, err)
		}
		return result, nil
	}
	if selected == nil {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	if selected.rules.ExcludeMuted {
		for i, input := range []tg.InputNotifyPeerClass{&tg.InputNotifyUsers{}, &tg.InputNotifyChats{}, &tg.InputNotifyBroadcasts{}} {
			settings, err := a.reads.api.AccountGetNotifySettings(ctx, input)
			if err != nil {
				return nil, readError(err)
			}
			if settings == nil {
				return nil, model.TextError(model.ErrorInvalidReference, nil)
			}
			until, _ := settings.GetMuteUntil() // Absent global mute means notifications enabled.
			if until < 0 {
				return nil, model.TextError(model.ErrorInvalidReference, nil)
			}
			selected.muted[i] = int64(until) > selected.now
		}
	}
	peers := make(map[model.PeerID]bool)
	position := model.DialogPosition{}
	visited := make(map[model.DialogPosition]bool)
	for pageIndex := 0; pageIndex < 100; pageIndex++ {
		if visited[position] {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		visited[position] = true
		page, err := a.dialogs(ctx, position, 100, selected)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Items {
			if peers[entry.Chat.ID] {
				return nil, model.TextError(model.ErrorInvalidReference, nil)
			}
			peers[entry.Chat.ID] = true
			if len(peers) > 100 {
				return nil, model.TextError(model.ErrorResultTooLarge, nil)
			}
		}
		if page.Next == nil {
			// Explicit entries must not disappear into unsupported or unavailable metadata.
			for peer := range selected.included {
				if !peers[peer] {
					return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
				}
			}
			selected.folder.Peers = make([]model.PeerID, 0, len(peers))
			for peer := range peers {
				selected.folder.Peers = append(selected.folder.Peers, peer)
			}
			sort.Slice(selected.folder.Peers, func(i, j int) bool { return selected.folder.Peers[i].String() < selected.folder.Peers[j].String() })
			return []model.Folder{selected.folder}, nil
		}
		position = *page.Next
	}
	return nil, model.TextError(model.ErrorResultTooLarge, nil)
}

func normalizeFolder(value tg.DialogFilterClass, self int64) (*folderFilter, error) {
	var rules tg.DialogFilter
	switch v := value.(type) {
	case *tg.DialogFilter:
		if v == nil {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		rules = *v
	case *tg.DialogFilterChatlist:
		if v == nil {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		rules = tg.DialogFilter{ID: v.ID, Title: v.Title, PinnedPeers: v.PinnedPeers, IncludePeers: v.IncludePeers}
	default:
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	if rules.ID < 2 || rules.ID > math.MaxInt32 || !utf8.ValidString(rules.Title.Text) || len(rules.Title.Text) > 4096 {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	filter := &folderFilter{folder: model.Folder{ID: int32(rules.ID), Title: rules.Title.Text}, rules: rules, included: make(map[model.PeerID]bool), excluded: make(map[model.PeerID]bool), now: time.Now().Unix()}
	for index, list := range [][]tg.InputPeerClass{rules.PinnedPeers, rules.IncludePeers, rules.ExcludePeers} {
		if len(list) > 200 {
			return nil, model.TextError(model.ErrorResultTooLarge, nil)
		}
		for _, input := range list {
			var peer tg.PeerClass
			switch p := input.(type) {
			case *tg.InputPeerSelf:
				peer = &tg.PeerUser{UserID: self}
			case *tg.InputPeerUser:
				if p != nil {
					peer = &tg.PeerUser{UserID: p.UserID}
				}
			case *tg.InputPeerChat:
				if p != nil {
					peer = &tg.PeerChat{ChatID: p.ChatID}
				}
			case *tg.InputPeerChannel:
				if p != nil {
					peer = &tg.PeerChannel{ChannelID: p.ChannelID}
				}
			default:
				return nil, model.TextError(model.ErrorInvalidReference, nil)
			}
			id, err := normalizePeerID(peer, self)
			if err != nil {
				return nil, err
			}
			if index == 2 {
				filter.excluded[id] = true
			} else {
				filter.included[id] = true
			}
		}
	}
	return filter, nil
}

func (f *folderFilter) matches(id model.PeerID, chat model.Chat, dialog *tg.Dialog, users []tg.UserClass) (bool, error) {
	if f.included[id] {
		return true, nil
	}
	if f.excluded[id] {
		return false, nil
	}
	category := 1
	included := f.rules.Groups
	switch id.Kind() {
	case model.PeerKindSelf:
		category = 0
		included = f.rules.Contacts
	case model.PeerKindUser:
		category = 0
		found := false
		for _, value := range users {
			user, ok := value.(*tg.User)
			if !ok || user.ID != id.TelegramID() {
				continue
			}
			found = true
			switch {
			case user.Bot:
				included = f.rules.Bots
			case user.Contact:
				included = f.rules.Contacts
			default:
				included = f.rules.NonContacts
			}
		}
		if !found {
			return false, model.TextError(model.ErrorInvalidReference, nil)
		}
	case model.PeerKindChannel:
		if chat.Broadcast {
			category = 2
			included = f.rules.Broadcasts
		}
	}
	if !included {
		return false, nil
	}
	folder, _ := dialog.GetFolderID()
	if f.rules.ExcludeArchived && folder == 1 {
		return false, nil
	}
	if dialog.UnreadMentionsCount < 0 {
		return false, model.TextError(model.ErrorInvalidReference, nil)
	}
	if dialog.UnreadMentionsCount == 0 {
		if f.rules.ExcludeRead && dialog.UnreadCount == 0 && !dialog.UnreadMark {
			return false, nil
		}
		if f.rules.ExcludeMuted {
			muted := f.muted[category]
			if until, ok := dialog.NotifySettings.GetMuteUntil(); ok {
				if until < 0 {
					return false, model.TextError(model.ErrorInvalidReference, nil)
				}
				muted = int64(until) > f.now
			}
			if muted {
				return false, nil
			}
		}
	}
	return true, nil
}
