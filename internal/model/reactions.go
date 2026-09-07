package model

import (
	"strconv"
	"unicode/utf8"
)

// Reactions is an untrusted aggregate snapshot, not a list of people or choices.
type Reactions struct {
	Minimal bool            `json:"minimal"`
	AsTags  bool            `json:"as_tags"`
	Counts  []ReactionCount `json:"counts"`
}

type ReactionCount struct {
	Kind          string `json:"kind"`
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
	Count         int    `json:"count"`
}

func (r *Reactions) Valid(peer PeerID) bool {
	if r == nil {
		return true
	}
	if r.Counts == nil || len(r.Counts) > 100 || (r.AsTags && peer.Kind() != PeerKindSelf) {
		return false
	}
	seen := make(map[string]bool, len(r.Counts))
	for _, count := range r.Counts {
		if count.Count < 0 || int64(count.Count) > 2147483647 {
			return false
		}
		var key string
		switch count.Kind {
		case "emoji":
			if count.Emoji == "" || len(count.Emoji) > 128 || !utf8.ValidString(count.Emoji) || count.CustomEmojiID != "" {
				return false
			}
			key = "emoji:" + count.Emoji
		case "custom_emoji":
			id, err := strconv.ParseInt(count.CustomEmojiID, 10, 64)
			if err != nil || id == 0 || strconv.FormatInt(id, 10) != count.CustomEmojiID || count.Emoji != "" {
				return false
			}
			key = "custom:" + count.CustomEmojiID
		case "paid":
			if count.Emoji != "" || count.CustomEmojiID != "" || r.AsTags {
				return false
			}
			key = "paid"
		default:
			return false
		}
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
