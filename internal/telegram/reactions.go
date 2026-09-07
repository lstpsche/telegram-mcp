package telegram

import (
	"strconv"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func normalizeReactions(peer model.PeerID, value tg.MessageReactions) (*model.Reactions, error) {
	invalid := func() (*model.Reactions, error) { return nil, model.TextError(model.ErrorInvalidReference, nil) }
	if len(value.Results) > 100 {
		return invalid()
	}
	result := &model.Reactions{
		Minimal: value.Min,
		AsTags:  value.ReactionsAsTags || (peer.Kind() == model.PeerKindSelf && len(value.Results) == 0),
		Counts:  make([]model.ReactionCount, 0, len(value.Results)),
	}
	for _, supplied := range value.Results {
		count := model.ReactionCount{Count: supplied.Count}
		switch reaction := supplied.Reaction.(type) {
		case *tg.ReactionEmoji:
			if reaction == nil {
				return invalid()
			}
			count.Kind, count.Emoji = "emoji", reaction.Emoticon
		case *tg.ReactionCustomEmoji:
			if reaction == nil {
				return invalid()
			}
			count.Kind, count.CustomEmojiID = "custom_emoji", strconv.FormatInt(reaction.DocumentID, 10)
		case *tg.ReactionPaid:
			if reaction == nil {
				return invalid()
			}
			count.Kind = "paid"
		default:
			return invalid()
		}
		result.Counts = append(result.Counts, count)
	}
	if !result.Valid(peer) {
		return invalid()
	}
	return result, nil
}
