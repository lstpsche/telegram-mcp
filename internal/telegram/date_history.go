package telegram

import (
	"math"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

// History is ordered newest first. Preserve dates for excluded candidates so a
// wholly filtered page can still prove that traversal crossed the lower bound.
func dateCandidates(peer model.PeerID, response tg.MessagesMessagesClass, candidates []model.Candidate) error {
	page, ok := response.(messagePage)
	if !ok || len(page.GetMessages()) != len(candidates) {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	previousDate, previousID := int64(math.MaxInt32), int64(math.MaxInt32)+1
	for i, raw := range page.GetMessages() {
		var date int
		switch message := raw.(type) {
		case *tg.Message:
			date = message.Date
			if !matchesPeer(peer, message.PeerID) {
				return model.TextError(model.ErrorInvalidReference, nil)
			}
		case *tg.MessageService:
			date = message.Date
			if !matchesPeer(peer, message.PeerID) {
				return model.TextError(model.ErrorInvalidReference, nil)
			}
		case *tg.MessageEmpty:
			// No timestamp evidence: consume this candidate without inferring completion.
		default:
			return model.TextError(model.ErrorInvalidReference, nil)
		}
		if int64(raw.GetID()) >= previousID {
			return model.TextError(model.ErrorInvalidReference, nil)
		}
		previousID = int64(raw.GetID())
		if _, empty := raw.(*tg.MessageEmpty); empty {
			continue
		}
		if date <= 0 || int64(date) > previousDate {
			return model.TextError(model.ErrorInvalidReference, nil)
		}
		previousDate = int64(date)
		candidates[i].SentAt = int64(date)
	}
	return nil
}
