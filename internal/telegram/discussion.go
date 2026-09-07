package telegram

import (
	"context"
	"math"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

// Discussion resolves a channel post to a joined ordinary supergroup's root.
// Incidental bodies, read counters and entities are not public results.
func (a *Account) Discussion(ctx context.Context, source model.MessageID, destination model.PeerID) (model.MessageID, error) {
	invalid := func() (model.MessageID, error) {
		return model.MessageID{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	if source.String() == "" || source.Peer().Kind() != model.PeerKindChannel || source.Peer().TopicID() != 0 || destination.Kind() != model.PeerKindChannel || destination.TopicID() != 0 || destination == source.Peer() {
		return model.MessageID{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if !a.Ready() {
		return model.MessageID{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	for _, peer := range []model.PeerID{source.Peer(), destination} {
		chat, err := a.Chat(ctx, peer)
		if err != nil {
			return model.MessageID{}, err
		}
		if chat.Forum || chat.Broadcast != (peer == source.Peer()) {
			return model.MessageID{}, model.TextError(model.ErrorUnsupportedPeer, nil)
		}
	}
	input, err := a.reads.inputPeer(ctx, source.Peer())
	if err != nil {
		return model.MessageID{}, err
	}
	response, err := a.reads.api.MessagesGetDiscussionMessage(ctx, &tg.MessagesGetDiscussionMessageRequest{Peer: input, MsgID: int(source.TelegramID())})
	if err != nil {
		return model.MessageID{}, readError(err)
	}
	if response == nil || len(response.Messages) == 0 || len(response.Messages) > 100 || len(response.Chats) > 200 || len(response.Users) > 200 {
		return invalid()
	}
	if err := validateDialogEntities(destination, response.Users, response.Chats); err != nil {
		return model.MessageID{}, err
	}
	var root *tg.Message
	for _, value := range response.Messages {
		m, ok := value.(*tg.Message)
		if !ok || m.ID <= 0 || m.ID > math.MaxInt32 {
			return invalid()
		}
		if !matchesPeer(destination, m.PeerID) {
			return invalid()
		}
		if root != nil && m.ID >= root.ID {
			return invalid()
		}
		root = m
	}
	from, ok := root.FwdFrom.FromID.(*tg.PeerChannel)
	if !ok || from.ChannelID != source.Peer().TelegramID() || root.FwdFrom.ChannelPost != int(source.TelegramID()) || root.FwdFrom.Imported || root.Noforwards || len(root.RestrictionReason) != 0 || root.TTLPeriod != 0 || root.FromScheduled || root.Date <= 0 {
		return invalid()
	}
	// Revalidate the destination after the remote lookup; never silently accept
	// a relink, a protected group, or a group the account no longer belongs to.
	chat, err := a.Chat(ctx, destination)
	if err != nil {
		return model.MessageID{}, err
	}
	if chat.Broadcast || chat.Forum {
		return model.MessageID{}, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	if err := a.reads.synchronize(ctx); err != nil {
		return model.MessageID{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return model.NewMessageID(destination, int32(root.ID))
}

// Telegram omits top_id for a direct reply to an ordinary supergroup's root.
func normalizeThreadRoot(peer model.PeerID, reply *tg.MessageReplyHeader, broadcast bool) (*model.MessageID, error) {
	top := reply.ReplyToTopID
	if top == 0 && !reply.Flags.Has(1) {
		if peer.Kind() != model.PeerKindChannel || (peer.TopicID() != 0 && !reply.ForumTopic) || broadcast {
			return nil, nil
		}
		top = reply.ReplyToMsgID
	}
	if top <= 0 || top > reply.ReplyToMsgID || top > math.MaxInt32 {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	root, err := model.NewMessageID(peer, int32(top))
	if err != nil {
		return nil, err
	}
	return &root, nil
}
