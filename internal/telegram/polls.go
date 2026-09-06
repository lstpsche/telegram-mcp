package telegram

import (
	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func normalizePoll(media *tg.MessageMediaPoll) (*model.Poll, error) {
	invalid := func() (*model.Poll, error) { return nil, model.TextError(model.ErrorInvalidReference, nil) }
	if media == nil {
		return invalid()
	}
	if media.AttachedMedia != nil || media.Flags.Has(0) {
		return nil, nil
	}
	p, r := media.Poll, media.Results
	if p.ID == 0 || len(p.Answers) < 2 || len(p.Answers) > 100 || len(r.Results) > 100 {
		return invalid()
	}
	out := &model.Poll{Question: p.Question.Text, Closed: p.Closed, PublicVoters: p.PublicVoters, MultipleChoice: p.MultipleChoice, Quiz: p.Quiz, Minimal: r.Min, Options: make([]model.PollOption, 0, len(p.Answers))}
	indices := make(map[string]int, len(p.Answers))
	for _, raw := range p.Answers {
		answer, ok := raw.(*tg.PollAnswer)
		if !ok || answer == nil || answer.Media != nil {
			return nil, nil
		}
		if len(answer.Option) == 0 || len(answer.Option) > 256 {
			return invalid()
		}
		key := string(answer.Option)
		if _, exists := indices[key]; exists {
			return invalid()
		}
		indices[key] = len(out.Options)
		out.Options = append(out.Options, model.PollOption{Text: answer.Text.Text})
	}
	if total, present := r.GetTotalVoters(); present {
		out.TotalVoters = &total
	}
	seen := map[string]bool{}
	if results, present := r.GetResults(); present {
		for _, result := range results {
			key := string(result.Option)
			index, ok := indices[key]
			if !ok || seen[key] {
				return invalid()
			}
			seen[key] = true
			if count, present := result.GetVoters(); present {
				out.Options[index].Voters = &count
			}
		}
	}
	if !out.Valid() {
		return invalid()
	}
	return out, nil
}
