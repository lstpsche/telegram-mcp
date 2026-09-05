package telegram

import (
	"context"
	"math"
	"time"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

// DiscoverSavedMessage opens a human-owned session and returns only the newest
// Saved Messages reference. Incidental message and draft content is discarded.
func (a *Account) DiscoverSavedMessage(ctx context.Context) (model.MessageID, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var message model.MessageID
	err := a.Observe(bounded, func(ctx context.Context, status AuthorizationStatus) error {
		if !status.Authorized {
			return model.TextError(model.ErrorNotReady, nil)
		}
		var err error
		message, err = a.latestSavedMessage(ctx)
		return err
	})
	if err != nil {
		return model.MessageID{}, err
	}
	return message, nil
}

func (a *Account) latestSavedMessage(ctx context.Context) (model.MessageID, error) {
	if !a.Ready() {
		return model.MessageID{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	peer, err := model.NewPeerID(model.PeerKindSelf, a.reads.self.Load())
	if err != nil {
		return model.MessageID{}, err
	}
	input, err := a.reads.inputPeer(bounded, peer)
	if err != nil {
		return model.MessageID{}, err
	}
	response, err := a.reads.api.MessagesGetPeerDialogs(bounded, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: input}})
	if err != nil {
		return model.MessageID{}, readError(err)
	}
	if len(response.Dialogs) != 1 || len(response.Messages) != 1 || len(response.Users) > 200 || len(response.Chats) != 0 {
		return model.MessageID{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	dialog, ok := response.Dialogs[0].(*tg.Dialog)
	if !ok || !matchesPeer(peer, dialog.Peer) || dialog.TopMessage <= 0 || dialog.TopMessage > math.MaxInt32 {
		return model.MessageID{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	message, ok := response.Messages[0].(*tg.Message)
	if !ok || message.ID != dialog.TopMessage || !matchesPeer(peer, message.PeerID) {
		return model.MessageID{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	if !validRemoteState(&response.State) {
		return model.MessageID{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if err := a.reads.waitCheckpoint(bounded, response.State.Pts, response.State.Qts, response.State.Seq); err != nil {
		return model.MessageID{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	if err := a.reads.synchronize(bounded); err != nil {
		return model.MessageID{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return model.NewMessageID(peer, int32(dialog.TopMessage))
}
