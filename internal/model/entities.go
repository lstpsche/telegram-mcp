package model

import (
	"math"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const MaximumTextEntities = 128

// TextEntity uses UTF-16 code-unit offsets into the unchanged message text.
// Links and identifiers are untrusted display metadata, never authority.
type TextEntity struct {
	Kind          string   `json:"kind"`
	Offset        int      `json:"offset"`
	Length        int      `json:"length"`
	URL           string   `json:"url,omitempty"`
	Language      string   `json:"language,omitempty"`
	User          string   `json:"user,omitempty"`
	CustomEmojiID string   `json:"custom_emoji_id,omitempty"`
	Collapsed     bool     `json:"collapsed,omitempty"`
	Date          *int64   `json:"date,omitempty"`
	DateFormat    []string `json:"date_format,omitempty"`
}

func ValidateTextEntities(text string, entities []TextEntity) error {
	if len(entities) > MaximumTextEntities {
		return TextError(ErrorResultTooLarge, nil)
	}
	if len(entities) == 0 {
		return nil
	}
	if len(text) > 64*1024 || !utf8.ValidString(text) {
		return TextError(ErrorInvalidReference, nil)
	}
	boundaries := make([]bool, len(text)+1)
	units := 0
	boundaries[0] = true
	for _, r := range text {
		units++
		if r > 0xffff {
			units++
		}
		boundaries[units] = true
	}
	for _, e := range entities {
		if e.Offset < 0 || e.Offset > units || e.Length <= 0 || e.Length > units-e.Offset || !boundaries[e.Offset] || !boundaries[e.Offset+e.Length] {
			return TextError(ErrorInvalidReference, nil)
		}
		if e.URL != "" && e.Kind != "text_url" || e.Language != "" && e.Kind != "pre" || e.User != "" && e.Kind != "mention_name" || e.CustomEmojiID != "" && e.Kind != "custom_emoji" || e.Collapsed && e.Kind != "blockquote" || (e.Date != nil || len(e.DateFormat) > 0) && e.Kind != "formatted_date" {
			return TextError(ErrorInvalidReference, nil)
		}
		switch e.Kind {
		case "unknown", "mention", "hashtag", "bot_command", "url", "email", "bold", "italic", "code", "phone", "cashtag", "underline", "strike", "bank_card", "spoiler", "blockquote":
		case "pre":
			if !validEntityString(e.Language, 128) {
				return TextError(ErrorInvalidReference, nil)
			}
		case "text_url":
			if e.URL == "" || !validEntityString(e.URL, 4096) {
				return TextError(ErrorInvalidReference, nil)
			}
		case "mention_name":
			p, err := ParsePeerID(e.User)
			if err != nil || p.Kind() != PeerKindUser {
				return TextError(ErrorInvalidReference, nil)
			}
		case "custom_emoji":
			id, err := strconv.ParseInt(e.CustomEmojiID, 10, 64)
			if err != nil || id == 0 || strconv.FormatInt(id, 10) != e.CustomEmojiID {
				return TextError(ErrorInvalidReference, nil)
			}
		case "formatted_date":
			if e.Date == nil || *e.Date < 0 || *e.Date > math.MaxInt32 || len(e.DateFormat) > 6 {
				return TextError(ErrorInvalidReference, nil)
			}
			seen := map[string]bool{}
			for _, f := range e.DateFormat {
				switch f {
				case "relative", "short_time", "long_time", "short_date", "long_date", "day_of_week":
				default:
					return TextError(ErrorInvalidReference, nil)
				}
				if seen[f] {
					return TextError(ErrorInvalidReference, nil)
				}
				seen[f] = true
			}
			if seen["relative"] && len(seen) > 1 || seen["short_time"] && seen["long_time"] || seen["short_date"] && seen["long_date"] {
				return TextError(ErrorInvalidReference, nil)
			}
		default:
			return TextError(ErrorInvalidReference, nil)
		}
	}
	return nil
}

func validEntityString(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
