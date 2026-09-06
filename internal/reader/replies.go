package reader

import (
	"context"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

// replyChain extends the already authorized window before its single receipt.
// A missing or withheld parent is deliberately indistinguishable to the caller.
func (s *Service) replyChain(ctx context.Context, query model.HistoryQuery, grant policy.Grant, authority mediaAuthority, items []model.Message, seen map[model.MessageID]bool) ([]model.Message, error) {
	target := -1
	for i := range items {
		if items[i].ID.TelegramID() == query.Target {
			target = i
			break
		}
	}
	if target < 0 {
		return nil, model.TextError(model.ErrorPolicyDenied, nil)
	}
	chain := &model.ReplyChain{State: "complete"}
	items[target].ReplyChain = chain
	current := items[target]
	for current.ReplyTo != nil {
		if !current.ValidReply() {
			return nil, model.TextError(model.ErrorInvalidReference, nil)
		}
		if chain.Depth == query.ReplyDepth {
			chain.State = "depth_limit"
			break
		}
		parent := *current.ReplyTo
		if err := s.checkGrantsCurrent(ctx, []policy.Grant{grant}); err != nil {
			return nil, err
		}
		if parent.TelegramID() < grant.MinID || parent.TelegramID() > grant.MaxID {
			chain.State = "unavailable"
			break
		}
		if err := grant.CheckRead(parent.TelegramID(), s.now()); err != nil {
			return nil, err
		}
		found := false
		for _, item := range items {
			if item.ID == parent {
				current = item
				found = true
				break
			}
		}
		if !found {
			if seen[parent] {
				chain.State = "unavailable"
				break
			}
			candidates, err := s.backend.History(ctx, model.HistoryQuery{Peer: query.Peer, Target: parent.TelegramID(), MinID: grant.MinID, MaxID: grant.MaxID, Limit: 1})
			if err != nil {
				return nil, err
			}
			if err := s.checkGrantsCurrent(ctx, []policy.Grant{grant}); err != nil {
				return nil, err
			}
			if len(candidates) > 1 {
				return nil, model.TextError(model.ErrorResultTooLarge, nil)
			}
			if len(candidates) == 0 {
				chain.State = "unavailable"
				break
			}
			candidate := candidates[0]
			if candidate.Message.ID != parent || !candidate.Message.ValidReply() {
				return nil, model.TextError(model.ErrorInvalidReference, nil)
			}
			if err := grant.CheckMessage(candidate, s.backend.SelfID(), s.now()); err != nil {
				switch model.TextErrorCategory(err) {
				case model.ErrorPolicyDenied, model.ErrorProtectedContent, model.ErrorEphemeralContent, model.ErrorUnsupportedPeer:
					chain.State = "unavailable"
				default:
					return nil, err
				}
				break
			}
			current, err = s.prepareMessage(candidate, grant, authority)
			if err != nil {
				return nil, err
			}
			items = append(items, current)
		}
		chain.Depth++
	}
	return items, nil
}
