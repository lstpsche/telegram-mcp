package app

import (
	"context"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func (a *Application) Topics(ctx context.Context, peer model.PeerID, position model.TopicPosition) (page model.TopicPage, err error) {
	err = a.withDiscovery(ctx, func(ctx context.Context, discovery humanDiscovery) error {
		topics, ok := discovery.(interface {
			DiscoverTopics(context.Context, model.PeerID, model.TopicPosition) (model.TopicPage, error)
		})
		if !ok {
			return ErrTextControlUnsupported
		}
		var err error
		page, err = topics.DiscoverTopics(ctx, peer, position)
		return err
	})
	if err != nil {
		return model.TopicPage{}, err
	}
	return page, nil
}
