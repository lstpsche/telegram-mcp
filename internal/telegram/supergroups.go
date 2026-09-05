package telegram

import (
	"context"
	"math"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func ordinarySupergroup(group *tg.Channel) bool {
	return group != nil && group.ID > 0 && group.Megagroup && !group.Broadcast && !group.Gigagroup && !group.Forum && !group.Monoforum && !group.Min && !group.Left && !group.Restricted && !group.Noforwards
}

// Channel hashes are adapter-owned navigation metadata, not content authority.
// Discovery may need an excluded channel as its next pagination offset.
func (r *readRuntime) saveChannels(ctx context.Context, groups []tg.ChatClass) error {
	if len(groups) > 200 {
		return model.TextError(model.ErrorResultTooLarge, nil)
	}
	for _, value := range groups {
		group, ok := value.(*tg.Channel)
		if !ok || group.Min || group.ID <= 0 {
			continue
		}
		hash, ok := group.GetAccessHash()
		if !ok || hash == 0 {
			continue
		}
		if err := r.storage.SetChannelAccessHash(ctx, r.self.Load(), group.ID, hash); err != nil {
			return model.TextError(model.ErrorFreshnessDegraded, err)
		}
	}
	return nil
}

// Supergroups are fetched live, without subscribing to independent channel pts.
// Their receipt RPC returns Bool, not affected pts. Verify both success and the
// server's exact dialog read position before the reader can release any body.
// A false Bool is a valid RPC result (also accepted by TDLib), not an RPC error.
func (a *Account) acknowledgeSupergroup(ctx context.Context, peer model.PeerID, input *tg.InputPeerChannel, through int32) error {
	_, err := a.reads.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: &tg.InputChannel{ChannelID: input.ChannelID, AccessHash: input.AccessHash}, MaxID: int(through)})
	if err != nil {
		return readError(err)
	}
	response, err := a.reads.api.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: input}})
	if err != nil {
		return readError(err)
	}
	if len(response.Dialogs) != 1 || len(response.Messages) > 1 || len(response.Users) > 200 || len(response.Chats) > 200 {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	dialog, ok := response.Dialogs[0].(*tg.Dialog)
	if !ok || !matchesPeer(peer, dialog.Peer) || dialog.ReadInboxMaxID < int(through) || dialog.ReadInboxMaxID > math.MaxInt32 {
		return model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if err := validateDialogEntities(peer, response.Users, response.Chats); err != nil {
		return err
	}
	if !validRemoteState(&response.State) {
		return model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if err := a.reads.waitCheckpoint(ctx, response.State.Pts, response.State.Qts, response.State.Seq); err != nil {
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	if err := a.reads.synchronize(ctx); err != nil {
		return model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return nil
}
