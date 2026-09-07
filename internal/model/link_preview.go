package model

import "unicode/utf8"

// LinkPreview is untrusted Telegram-supplied display data, never URL authority.
type LinkPreview struct {
	State       string `json:"state"`
	Manual      bool   `json:"manual"`
	URL         string `json:"url,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

func (p *LinkPreview) Valid() bool {
	if p == nil {
		return true
	}
	switch p.State {
	case "available":
		if p.URL == "" {
			return false
		}
	case "pending", "unavailable":
		if p.SiteName != "" || p.Title != "" || p.Description != "" {
			return false
		}
	default:
		return false
	}
	for _, field := range []string{p.URL, p.SiteName, p.Title, p.Description} {
		if len(field) > 4096 || !utf8.ValidString(field) {
			return false
		}
	}
	return true
}
