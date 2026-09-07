package reader

import (
	"context"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type discussionBackend interface {
	Discussion(context.Context, model.MessageID, model.PeerID) (model.MessageID, error)
}

// Discussion resolution returns navigation only. Its RPC cannot constrain the
// incidental messages by grant range or author, so it requires Full read.
func (s *Service) resolveDiscussion(ctx context.Context, lease *policy.Lease, grant policy.Grant, message model.Message) (*model.MessageID, error) {
	backend, ok := s.backend.(discussionBackend)
	if !ok {
		return nil, model.TextError(model.ErrorNotReady, nil)
	}
	peer, err := model.ParsePeerID(message.DiscussionPeer)
	if err != nil || !message.ValidReply() || message.ChannelPost == nil {
		return nil, model.TextError(model.ErrorUnsupportedPeer, nil)
	}
	destination, err := lease.Grant(ctx, peer)
	if err != nil {
		return nil, err
	}
	if err := s.checkGrantsCurrent(ctx, []policy.Grant{grant, destination}); err != nil {
		return nil, err
	}
	root, err := backend.Discussion(ctx, message.ID, peer)
	if err != nil {
		return nil, err
	}
	if root.String() == "" || root.Peer() != peer || root.TelegramID() < destination.MinID || root.TelegramID() > destination.MaxID {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	// Recheck the source before a potentially stale mapping is released.
	candidates, err := s.backend.History(ctx, model.HistoryQuery{Peer: message.ID.Peer(), Target: message.ID.TelegramID(), MinID: grant.MinID, MaxID: grant.MaxID, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(candidates) != 1 || candidates[0].Message.ID != message.ID || candidates[0].Message.DiscussionPeer != message.DiscussionPeer {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := grant.CheckMessage(candidates[0], s.backend.SelfID(), s.now()); err != nil {
		return nil, err
	}
	if err := s.checkGrantsCurrent(ctx, []policy.Grant{grant, destination}); err != nil {
		return nil, err
	}
	return &root, nil
}
