package telegram

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func sendableUser(user *tg.User) bool {
	return user != nil && user.ID > 0 && !user.Deleted && !user.Min
}
func sendableChat(chat *tg.Chat) bool {
	return chat != nil && chat.ID > 0 && !chat.Deactivated && !chat.Left && chat.MigratedTo == nil
}
func sendableChannel(channel *tg.Channel) bool {
	return channel != nil && channel.ID > 0 && !channel.Min && !channel.Left && !channel.Gigagroup && !channel.Monoforum && (channel.Broadcast != channel.Megagroup) && (!channel.Forum || channel.Megagroup)
}
func matchedUsername(primary string, aliases []tg.Username, requested string) bool {
	if strings.EqualFold(primary, requested) {
		return true
	}
	for _, alias := range aliases {
		if alias.Active && strings.EqualFold(alias.Username, requested) {
			return true
		}
	}
	return false
}
func recipientDisplay(value string) string {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return value
}
func recipientUnavailable() error {
	return &model.SendError{Category: model.ErrorRecipientUnavailable, FailureCode: model.SendFailureRecipientUnavailable}
}

func recipientWriteForbidden() error {
	return &model.SendError{Category: model.ErrorWriteForbidden, FailureCode: model.SendFailureWriteForbidden}
}

func plainTextBanned(rights tg.ChatBannedRights, now time.Time) bool {
	return (rights.ViewMessages || rights.SendMessages || rights.SendPlain) && (rights.UntilDate <= 0 || int64(rights.UntilDate) >= now.Unix())
}

func chatTextForbidden(chat *tg.Chat, now time.Time) bool {
	admin, hasAdmin := chat.GetAdminRights()
	if chat.Creator || hasAdmin && !admin.Zero() {
		return false
	}
	rights, hasRights := chat.GetDefaultBannedRights()
	return hasRights && plainTextBanned(rights, now)
}

func channelTextForbidden(channel *tg.Channel, now time.Time) bool {
	admin, hasAdmin := channel.GetAdminRights()
	// Group sends use the logged-in user. Anonymous admins can only post as a
	// channel, even when they also own the group.
	if channel.Megagroup && hasAdmin && admin.Anonymous {
		return true
	}
	if channel.Creator {
		return false
	}
	if channel.Broadcast {
		return !hasAdmin || !admin.PostMessages
	}
	if hasAdmin && !admin.Zero() {
		return false
	}
	rights, hasRights := channel.GetBannedRights()
	// Supergroup default restrictions may be bypassed by boosts. The required
	// per-user boost counts live in channelFull, not this channel metadata.
	return hasRights && plainTextBanned(rights, now)
}

func closedTopicTextForbidden(channel *tg.Channel, topic *tg.ForumTopic) bool {
	admin, hasAdmin := channel.GetAdminRights()
	return topic.Closed && !topic.My && !channel.Creator && !(hasAdmin && admin.ManageTopics)
}

func (a *Account) ResolveRecipient(ctx context.Context, query model.ResolveRecipientQuery) (model.ResolvedRecipient, error) {
	query, err := query.Normalize()
	if err != nil {
		return model.ResolvedRecipient{}, err
	}
	if !a.Ready() || a.reads.sending == nil {
		return model.ResolvedRecipient{}, model.TextError(model.ErrorNotReady, nil)
	}
	if err := ctx.Err(); err != nil {
		return model.ResolvedRecipient{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	ctx = context.WithValue(ctx, outgoingContextKey{}, &outgoingCall{peer: query.Peer})
	if err := a.reads.storage.checkEpoch(ctx); err != nil {
		return model.ResolvedRecipient{}, a.reads.metadataError(err)
	}
	if query.Peer.String() != "" {
		result, _, err := a.resolveKnownRecipient(ctx, query.Peer, false)
		return result, err
	}
	var response *tg.ContactsResolvedPeer
	if query.Phone != "" {
		response, err = a.reads.api.ContactsResolvePhone(ctx, query.Phone)
	} else {
		response, err = a.reads.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: query.Username})
	}
	if err != nil {
		return model.ResolvedRecipient{}, outgoingError(err)
	}
	if response == nil {
		return model.ResolvedRecipient{}, recipientUnavailable()
	}
	var result model.ResolvedRecipient
	switch peer := response.Peer.(type) {
	case *tg.PeerUser:
		if len(response.Users) != 1 || len(response.Chats) != 0 {
			return result, recipientUnavailable()
		}
		user, ok := response.Users[0].(*tg.User)
		if !ok || !sendableUser(user) || user.ID != peer.UserID || query.Username != "" && !matchedUsername(user.Username, user.Usernames, query.Username) || query.Phone != "" && user.Phone != "" && strings.TrimPrefix(query.Phone, "+") != user.Phone {
			return result, recipientUnavailable()
		}
		kind := model.PeerKindUser
		if user.ID == a.reads.self.Load() {
			kind = model.PeerKindSelf
		}
		result.Peer, err = model.NewPeerID(kind, user.ID)
		result.Kind = kind
		result.DisplayName = recipientDisplay(strings.TrimSpace(user.FirstName + " " + user.LastName))
		if kind != model.PeerKindSelf {
			hash, has := user.GetAccessHash()
			if !has || hash == 0 {
				return model.ResolvedRecipient{}, recipientUnavailable()
			}
			if err = a.reads.storage.SetUserAccessHash(ctx, a.reads.self.Load(), user.ID, hash); err != nil {
				return model.ResolvedRecipient{}, a.reads.metadataError(err)
			}
		}
	case *tg.PeerChannel:
		if query.Phone != "" || len(response.Chats) != 1 || len(response.Users) != 0 {
			return result, recipientUnavailable()
		}
		channel, ok := response.Chats[0].(*tg.Channel)
		if !ok || !sendableChannel(channel) || channel.ID != peer.ChannelID || !matchedUsername(channel.Username, channel.Usernames, query.Username) {
			return result, recipientUnavailable()
		}
		hash, has := channel.GetAccessHash()
		if !has || hash == 0 {
			return result, recipientUnavailable()
		}
		result.Peer, err = model.NewPeerID(model.PeerKindChannel, channel.ID)
		result.Kind = model.PeerKindChannel
		result.DisplayName = recipientDisplay(channel.Title)
		if err = a.reads.storage.SetChannelAccessHash(ctx, a.reads.self.Load(), channel.ID, hash); err != nil {
			return model.ResolvedRecipient{}, a.reads.metadataError(err)
		}
	default:
		return result, recipientUnavailable()
	}
	if err != nil {
		return model.ResolvedRecipient{}, recipientUnavailable()
	}
	if query.Username != "" {
		result.Username = query.Username
	}
	if err := result.Validate(); err != nil {
		return model.ResolvedRecipient{}, recipientUnavailable()
	}
	return result, nil
}

// resolveKnownRecipient refreshes precisely one entity. Read-content eligibility
// is deliberately separate: protected history does not prohibit a new send.
func (a *Account) resolveKnownRecipient(ctx context.Context, peer model.PeerID, forSend bool) (model.ResolvedRecipient, bool, error) {
	result := model.ResolvedRecipient{Peer: peer, Kind: peer.Kind()}
	input, err := a.reads.inputPeer(ctx, peer)
	if err != nil {
		return result, false, err
	}
	broadcast := false
	switch input := input.(type) {
	case *tg.InputPeerSelf:
		result.DisplayName = "Saved Messages"
	case *tg.InputPeerUser:
		users, e := a.reads.api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: input.UserID, AccessHash: input.AccessHash}})
		if e != nil {
			return result, false, outgoingError(e)
		}
		if len(users) != 1 {
			return result, false, recipientUnavailable()
		}
		user, ok := users[0].(*tg.User)
		if !ok || !sendableUser(user) || user.ID != peer.TelegramID() {
			return result, false, recipientUnavailable()
		}
		hash, has := user.GetAccessHash()
		if !has || hash == 0 {
			return result, false, recipientUnavailable()
		}
		if err = a.reads.storage.SetUserAccessHash(ctx, a.reads.self.Load(), user.ID, hash); err != nil {
			return result, false, a.reads.metadataError(err)
		}
		result.DisplayName = recipientDisplay(strings.TrimSpace(user.FirstName + " " + user.LastName))
	case *tg.InputPeerChat:
		chats, e := a.reads.api.MessagesGetChats(ctx, []int64{input.ChatID})
		if e != nil {
			return result, false, outgoingError(e)
		}
		if len(chats.GetChats()) != 1 {
			return result, false, recipientUnavailable()
		}
		chat, ok := chats.GetChats()[0].(*tg.Chat)
		if !ok || !sendableChat(chat) || chat.ID != peer.TelegramID() {
			return result, false, recipientUnavailable()
		}
		if forSend && chatTextForbidden(chat, time.Now()) {
			return result, false, recipientWriteForbidden()
		}
		result.DisplayName = recipientDisplay(chat.Title)
	case *tg.InputPeerChannel:
		chats, e := a.reads.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: input.ChannelID, AccessHash: input.AccessHash}})
		if e != nil {
			return result, false, outgoingError(e)
		}
		if len(chats.GetChats()) != 1 {
			return result, false, recipientUnavailable()
		}
		channel, ok := chats.GetChats()[0].(*tg.Channel)
		if !ok || !sendableChannel(channel) || channel.ID != peer.TelegramID() || (peer.TopicID() != 0 && !channel.Forum) {
			return result, false, recipientUnavailable()
		}
		if forSend && channelTextForbidden(channel, time.Now()) {
			return result, false, recipientWriteForbidden()
		}
		hash, has := channel.GetAccessHash()
		if !has || hash == 0 {
			return result, false, recipientUnavailable()
		}
		if err = a.reads.storage.SetChannelAccessHash(ctx, a.reads.self.Load(), channel.ID, hash); err != nil {
			return result, false, a.reads.metadataError(err)
		}
		broadcast = channel.Broadcast
		result.DisplayName = recipientDisplay(channel.Title)
		if peer.TopicID() != 0 {
			topics, e := a.reads.api.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: input, Topics: []int{int(peer.TopicID())}})
			if e != nil {
				return result, false, outgoingError(e)
			}
			if topics == nil || len(topics.Topics) != 1 || len(topics.Messages) > 100 || len(topics.Users) > 200 || len(topics.Chats) > 200 {
				return result, false, recipientUnavailable()
			}
			topic, ok := topics.Topics[0].(*tg.ForumTopic)
			if !ok || topic.Short || topic.ID != int(peer.TopicID()) || !matchesPeer(peer, topic.Peer) || topic.Hidden && topic.ID != 1 {
				return result, false, recipientUnavailable()
			}
			if forSend && closedTopicTextForbidden(channel, topic) {
				return result, false, recipientWriteForbidden()
			}
			result.DisplayName = recipientDisplay(topic.Title)
		} else if channel.Forum && forSend {
			return result, false, recipientUnavailable()
		}
	default:
		return result, false, recipientUnavailable()
	}
	if result.Validate() != nil {
		return model.ResolvedRecipient{}, false, recipientUnavailable()
	}
	return result, broadcast, nil
}
