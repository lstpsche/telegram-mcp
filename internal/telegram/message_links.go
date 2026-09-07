package telegram

import (
	"context"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

// ResolveMessagePublisher resolves navigation only; callers require Full read
// before this lookup and independently authorize the resulting immutable peer.
func (a *Account) ResolveMessagePublisher(ctx context.Context, username string) (model.Chat, error) {
	if link, err := model.ParseMessageLink("https://t.me/" + username + "/1"); err != nil || link.Username != username {
		return model.Chat{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if !a.Ready() {
		return model.Chat{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	response, err := a.reads.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return model.Chat{}, readError(err)
	}
	if response == nil || len(response.Chats) != 1 || len(response.Users) != 0 {
		return model.Chat{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	peer, ok := response.Peer.(*tg.PeerChannel)
	if !ok {
		return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	channel, ok := response.Chats[0].(*tg.Channel)
	if !ok || channel.ID != peer.ChannelID || !(ordinarySupergroup(channel) || ordinaryBroadcast(channel) || forumGroup(channel)) {
		return model.Chat{}, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	matched := strings.EqualFold(channel.Username, username)
	for _, alias := range channel.Usernames {
		if alias.Active && strings.EqualFold(alias.Username, username) {
			matched = true
		}
	}
	if !matched {
		return model.Chat{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	id, err := model.NewPeerID(model.PeerKindChannel, peer.ChannelID)
	if err != nil {
		return model.Chat{}, model.TextError(model.ErrorInvalidReference, err)
	}
	if err := a.reads.saveChannels(ctx, response.Chats); err != nil {
		return model.Chat{}, err
	}
	if err := a.reads.synchronize(ctx); err != nil {
		return model.Chat{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return model.Chat{ID: id, Forum: channel.Forum, Broadcast: channel.Broadcast}, nil
}
