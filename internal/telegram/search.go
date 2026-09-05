package telegram

import (
	"context"
	"math"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

// Search always names a supported dialog. Empty peers and account-wide search
// are unavailable. Candidate bodies remain internal; the reader emits snippets.
func (a *Account) Search(ctx context.Context, q model.SearchQuery) ([]model.Candidate, error) {
	if !a.Ready() {
		return nil, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	query, err := model.NormalizeSearchQuery(q.Query)
	if err != nil {
		return nil, err
	}
	if q.MinID <= 0 || q.MaxID < q.MinID || q.Before < 0 || (q.Before > 0 && q.Before <= q.MinID) || model.ValidatePageSize(q.Limit) != nil {
		return nil, model.TextError(model.ErrorInvalidInput, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	if _, err := a.Chat(bounded, q.Peer); err != nil {
		return nil, err
	}
	input, err := a.reads.inputPeer(bounded, q.Peer)
	if err != nil {
		return nil, err
	}
	offset, maximum := int(q.Before), 0
	if q.MaxID < math.MaxInt32 {
		maximum = int(q.MaxID) + 1
		if offset == 0 || offset > maximum {
			offset = maximum
		}
	}
	response, err := a.reads.api.MessagesSearch(bounded, &tg.MessagesSearchRequest{Peer: input, Q: query, Filter: &tg.InputMessagesFilterEmpty{}, OffsetID: offset, Limit: q.Limit, MinID: int(q.MinID) - 1, MaxID: maximum})
	if err != nil {
		return nil, readError(err)
	}
	// Telegram's empty search windows can omit every auxiliary entity,
	// including for supergroups. Recheck the exact peer and synchronize after
	// that successful response; nonempty pages still require their own entities.
	var emptyPage messagePage
	switch page := response.(type) {
	case *tg.MessagesMessages:
		emptyPage = page
	case *tg.MessagesMessagesSlice:
		emptyPage = page
	}
	if emptyPage != nil && len(emptyPage.GetMessages()) == 0 && len(emptyPage.GetUsers()) == 0 && len(emptyPage.GetChats()) == 0 && len(emptyPage.GetTopics()) == 0 {
		if _, err := a.Chat(bounded, q.Peer); err != nil {
			return nil, err
		}
		return []model.Candidate{}, nil
	}
	candidates, err := a.reads.normalizePage(bounded, q.Peer, response, q.Limit)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		id := candidate.Message.ID.TelegramID()
		if id < q.MinID || id > q.MaxID || (q.Before > 0 && id >= q.Before) {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
	}
	if err := a.reads.synchronize(bounded); err != nil {
		return nil, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return candidates, nil
}

// Unread discards incidental top messages/drafts from the scoped dialog RPC.
// No history or content acknowledgment is issued by this metadata operation.
func (a *Account) Unread(ctx context.Context, peer model.PeerID) (model.Unread, error) {
	if !a.Ready() {
		return model.Unread{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	if _, err := a.Chat(bounded, peer); err != nil {
		return model.Unread{}, err
	}
	input, err := a.reads.inputPeer(bounded, peer)
	if err != nil {
		return model.Unread{}, err
	}
	response, err := a.reads.api.MessagesGetPeerDialogs(bounded, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: input}})
	if err != nil {
		return model.Unread{}, readError(err)
	}
	if len(response.Dialogs) != 1 || len(response.Messages) > 1 || len(response.Users) > 200 || len(response.Chats) > 200 {
		return model.Unread{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	dialog, ok := response.Dialogs[0].(*tg.Dialog)
	if !ok || !matchesPeer(peer, dialog.Peer) || dialog.UnreadCount < 0 || dialog.UnreadCount > math.MaxInt32 {
		return model.Unread{}, model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := validateDialogEntities(peer, response.Users, response.Chats); err != nil {
		return model.Unread{}, err
	}
	if !validRemoteState(&response.State) {
		return model.Unread{}, model.TextError(model.ErrorFreshnessDegraded, nil)
	}
	if err := a.reads.saveUsers(bounded, response.Users); err != nil {
		return model.Unread{}, err
	}
	if err := a.reads.waitCheckpoint(bounded, response.State.Pts, response.State.Qts, response.State.Seq); err != nil {
		return model.Unread{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	if err := a.reads.synchronize(bounded); err != nil {
		return model.Unread{}, model.TextError(model.ErrorFreshnessDegraded, err)
	}
	return model.Unread{Peer: peer, Count: dialog.UnreadCount, Marked: dialog.UnreadMark}, nil
}
