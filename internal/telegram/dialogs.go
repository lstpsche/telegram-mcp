package telegram

import (
	"context"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func (r *readRuntime) peerID(value tg.PeerClass) (model.PeerID, error) {
	return normalizePeerID(value, r.self.Load())
}

func normalizePeerID(value tg.PeerClass, self int64) (model.PeerID, error) {
	switch p := value.(type) {
	case *tg.PeerUser:
		if p == nil {
			return model.PeerID{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		kind := model.PeerKindUser
		if p.UserID == self {
			kind = model.PeerKindSelf
		}
		return model.NewPeerID(kind, p.UserID)
	case *tg.PeerChat:
		if p == nil {
			return model.PeerID{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		return model.NewPeerID(model.PeerKindChat, p.ChatID)
	case *tg.PeerChannel:
		if p == nil {
			return model.PeerID{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		return model.NewPeerID(model.PeerKindChannel, p.ChannelID)
	default:
		return model.PeerID{}, model.TextError(model.ErrorInvalidReference, nil)
	}
}

// Dialogs requires account-wide authority or explicit human peer discovery. A page consumes at
// most limit remote dialogs, including unsupported entries. Telegram may return
// more candidates; bound that response separately and resume after the consumed window.
func (a *Account) Dialogs(ctx context.Context, position model.DialogPosition, limit int) (model.DialogPage, error) {
	if !a.Ready() {
		return model.DialogPage{}, model.TextError(model.ErrorNotReady, nil)
	}
	if !position.Valid() || model.ValidatePageSize(limit) != nil {
		return model.DialogPage{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	var input tg.InputPeerClass = &tg.InputPeerEmpty{}
	if position.Peer != "" && !position.Pinned {
		peer, err := model.ParsePeerID(position.Peer)
		if err != nil {
			return model.DialogPage{}, err
		}
		input, err = a.reads.inputPeer(bounded, peer)
		if err != nil {
			return model.DialogPage{}, err
		}
	}
	request := &tg.MessagesGetDialogsRequest{OffsetPeer: input, Limit: limit}
	if !position.Pinned {
		request.OffsetID, request.OffsetDate = int(position.MessageID), int(position.Date)
		request.ExcludePinned = position.Peer != ""
	}
	request.SetFolderID(position.Folder)
	response, err := a.reads.api.MessagesGetDialogs(bounded, request)
	if err != nil {
		return model.DialogPage{}, readError(err)
	}
	var dialogs []tg.DialogClass
	var users []tg.UserClass
	var groups []tg.ChatClass
	var messages []tg.MessageClass
	complete := false
	switch page := response.(type) {
	case *tg.MessagesDialogs:
		dialogs, users, groups, messages = page.Dialogs, page.Users, page.Chats, page.Messages
		complete = true
	case *tg.MessagesDialogsSlice:
		dialogs, users, groups, messages = page.Dialogs, page.Users, page.Chats, page.Messages
		if page.Count < len(dialogs) {
			return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		complete = len(dialogs) == 0
	default:
		return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	if len(dialogs) > 200 || len(messages) > 200 || len(users) > 200 || len(groups) > 200 {
		return model.DialogPage{}, model.TextError(model.ErrorResultTooLarge, nil)
	}
	// Pinned order is independent of message dates. While a page ends inside
	// that prefix, refetch it and resume after the exact pinned peer. Once an
	// ordinary dialog is consumed, use Telegram's offset triple and exclude pins.
	start := 0
	seen := make(map[model.PeerID]bool)
	ordinary := false
	folderDialogs := make([]tg.DialogClass, 0, len(dialogs))
	for _, value := range dialogs {
		dialog, ok := value.(*tg.Dialog)
		if !ok {
			return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		id, err := a.reads.peerID(dialog.Peer)
		if err != nil || seen[id] || dialog.TopMessage <= 0 || dialog.TopMessage > math.MaxInt32 || dialog.UnreadCount < 0 || dialog.UnreadCount > math.MaxInt32 {
			return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, err)
		}
		seen[id] = true
		folder, _ := dialog.GetFolderID() // An absent folder is the main list.
		if folder < 0 || folder > 1 || (dialog.Pinned && request.ExcludePinned) {
			return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		if folder != position.Folder {
			// Telegram can prepend pins from another folder. They are not
			// candidates or continuation boundaries for the requested list.
			if !dialog.Pinned {
				return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
			}
			continue
		}
		folderDialogs = append(folderDialogs, dialog)
		if dialog.Pinned && ordinary {
			return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		ordinary = ordinary || !dialog.Pinned
		if position.Pinned && id.String() == position.Peer {
			if !dialog.Pinned {
				return model.DialogPage{}, model.TextError(model.ErrorCursorInvalid, nil)
			}
			start = len(folderDialogs)
		}
	}
	if position.Pinned && start == 0 {
		return model.DialogPage{}, model.TextError(model.ErrorCursorInvalid, nil)
	}
	dialogs = folderDialogs
	end := min(start+limit, len(dialogs))
	if end < len(dialogs) {
		complete = false
	}
	dialogs = dialogs[start:end]
	if err := a.reads.saveUsers(bounded, users); err != nil {
		return model.DialogPage{}, err
	}
	if err := a.reads.saveChannels(bounded, groups); err != nil {
		return model.DialogPage{}, err
	}
	supported := make(map[model.PeerID]model.Chat)
	self, err := model.NewPeerID(model.PeerKindSelf, a.reads.self.Load())
	if err != nil {
		return model.DialogPage{}, err
	}
	supported[self] = model.Chat{ID: self, Title: "Saved Messages"}
	for _, value := range users {
		user, ok := value.(*tg.User)
		if !ok || !readableUser(user) || user.ID == self.TelegramID() {
			continue
		}
		if hash, ok := user.GetAccessHash(); !ok || hash == 0 {
			continue
		}
		id, err := model.NewPeerID(model.PeerKindUser, user.ID)
		if err != nil {
			return model.DialogPage{}, err
		}
		supported[id] = model.Chat{ID: id, Title: strings.TrimSpace(user.FirstName + " " + user.LastName)}
	}
	for _, value := range groups {
		var id model.PeerID
		var chat model.Chat
		switch group := value.(type) {
		case *tg.Chat:
			if !ordinaryChat(group) {
				continue
			}
			id, err = model.NewPeerID(model.PeerKindChat, group.ID)
			chat.Title = group.Title
		case *tg.Channel:
			if !(ordinarySupergroup(group) || ordinaryBroadcast(group) || forumGroup(group)) {
				continue
			}
			if hash, ok := group.GetAccessHash(); !ok || hash == 0 {
				continue
			}
			id, err = model.NewPeerID(model.PeerKindChannel, group.ID)
			chat.Title = group.Title
			chat.Forum, chat.Broadcast = group.Forum, group.Broadcast
		default:
			continue
		}
		if err != nil {
			return model.DialogPage{}, err
		}
		chat.ID = id
		supported[id] = chat
	}
	result := model.DialogPage{Items: make([]model.DialogEntry, 0, len(dialogs)), Scanned: len(dialogs)}
	var last *tg.Dialog
	for _, value := range dialogs {
		dialog := value.(*tg.Dialog) // Validated before selecting the bounded window.
		id, err := a.reads.peerID(dialog.Peer)
		if err != nil {
			return model.DialogPage{}, err
		}
		last = dialog
		chat, ok := supported[id]
		if !ok {
			continue
		}
		if !utf8.ValidString(chat.Title) || len(chat.Title) > 4096 {
			return model.DialogPage{}, model.TextError(model.ErrorResultTooLarge, nil)
		}
		result.Items = append(result.Items, model.DialogEntry{Chat: chat, Unread: model.Unread{Peer: id, Count: dialog.UnreadCount, Marked: dialog.UnreadMark}})
	}
	if !complete {
		if last == nil {
			return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		peer, err := a.reads.peerID(last.Peer)
		if err != nil {
			return model.DialogPage{}, err
		}
		var date int
		for _, value := range messages {
			// Service messages also provide a valid pagination boundary.
			message, ok := value.AsNotEmpty()
			if ok && message.GetID() == last.TopMessage && matchesPeer(peer, message.GetPeerID()) {
				if date != 0 {
					return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
				}
				date = message.GetDate()
			}
		}
		if date <= 0 || date > math.MaxInt32 {
			return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		next := model.DialogPosition{Folder: position.Folder, Peer: peer.String(), MessageID: int32(last.TopMessage), Date: int32(date), Pinned: last.Pinned}
		if next == position {
			return model.DialogPage{}, model.TextError(model.ErrorInvalidReference, nil)
		}
		// Prove that continuation can be resolved without exposing an access hash.
		if _, err := a.reads.inputPeer(bounded, peer); err != nil {
			return model.DialogPage{}, err
		}
		result.Next = &next
	} else if position.Folder == 0 {
		result.Next = &model.DialogPosition{Folder: 1}
	}
	if err := a.reads.synchronize(bounded); err != nil {
		return model.DialogPage{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return result, nil
}
