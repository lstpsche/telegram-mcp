package model

import "unicode/utf8"

// Poll is an untrusted snapshot. Missing counts are unknown, never zero.
type Poll struct {
	Question       string       `json:"question"`
	Options        []PollOption `json:"options"`
	Closed         bool         `json:"closed"`
	PublicVoters   bool         `json:"public_voters"`
	MultipleChoice bool         `json:"multiple_choice"`
	Quiz           bool         `json:"quiz"`
	Minimal        bool         `json:"minimal"`
	TotalVoters    *int         `json:"total_voters,omitempty"`
}

type PollOption struct {
	Text   string `json:"text"`
	Voters *int   `json:"voters,omitempty"`
}

func (p *Poll) Valid() bool {
	if p == nil {
		return true
	}
	if !pollText(p.Question) || len(p.Options) < 2 || len(p.Options) > 100 || !pollCount(p.TotalVoters) {
		return false
	}
	for _, option := range p.Options {
		if !pollText(option.Text) || !pollCount(option.Voters) || (option.Voters != nil && p.TotalVoters != nil && *option.Voters > *p.TotalVoters) {
			return false
		}
	}
	return true
}
func pollText(s string) bool { return s != "" && len(s) <= 4096 && utf8.ValidString(s) }
func pollCount(n *int) bool  { return n == nil || (*n >= 0 && int64(*n) <= 2147483647) }
