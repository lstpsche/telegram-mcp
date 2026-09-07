package reader

import (
	"context"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type messagePublisherBackend interface {
	ResolveMessagePublisher(context.Context, string) (model.Chat, error)
}

// ContextReference parses either a strict ID or a supported Telegram URL. It
// performs no I/O; public publisher lookup remains inside the policy lease.
func ContextReference(value string, options model.HistoryQuery) (model.HistoryQuery, error) {
	if id, err := model.ParseMessageID(value); err == nil {
		options.Peer = id.Peer()
		options.Target = id.TelegramID()
		return options, nil
	}
	link, err := model.ParseMessageLink(value)
	if err != nil {
		return model.HistoryQuery{}, err
	}
	options.Target = link.Message
	if link.Username != "" {
		options.LinkUsername = link.Username
		options.LinkTopic = link.Topic
		return options, nil
	}
	peer, err := model.NewPeerID(model.PeerKindChannel, link.Channel)
	if err != nil {
		return model.HistoryQuery{}, model.TextError(model.ErrorInvalidInput, nil)
	}
	if link.Topic != 0 {
		peer, err = model.NewTopicPeer(peer, link.Topic)
		if err != nil {
			return model.HistoryQuery{}, model.TextError(model.ErrorInvalidInput, nil)
		}
	}
	options.Peer = peer
	return options, nil
}

func (s *Service) resolveContextPublisher(ctx context.Context, lease *policy.Lease, q model.HistoryQuery) (model.HistoryQuery, error) {
	full, err := lease.FullRead(ctx)
	if err != nil {
		return model.HistoryQuery{}, err
	}
	if !full {
		return model.HistoryQuery{}, model.TextError(model.ErrorPolicyDenied, nil)
	}
	backend, ok := s.backend.(messagePublisherBackend)
	if !ok {
		return model.HistoryQuery{}, model.TextError(model.ErrorNotReady, nil)
	}
	chat, err := backend.ResolveMessagePublisher(ctx, q.LinkUsername)
	if err != nil {
		return model.HistoryQuery{}, err
	}
	if chat.ID.String() == "" || chat.ID.Kind() != model.PeerKindChannel || chat.ID.TopicID() != 0 || chat.Forum != (q.LinkTopic != 0) {
		return model.HistoryQuery{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	q.Peer = chat.ID
	if q.LinkTopic != 0 {
		q.Peer, err = model.NewTopicPeer(q.Peer, q.LinkTopic)
		if err != nil {
			return model.HistoryQuery{}, err
		}
	}
	q.LinkUsername = ""
	q.LinkTopic = 0
	return q, nil
}
