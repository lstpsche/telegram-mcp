package model

import (
	"time"
	"unicode/utf8"
)

// Forward is untrusted attribution of a copy, never authority over its origin.
type Forward struct {
	Date       string `json:"date"`
	FromPeer   string `json:"from_peer,omitempty"`
	FromName   string `json:"from_name,omitempty"`
	PostAuthor string `json:"post_author,omitempty"`
}

func (f Forward) Validate() error {
	date, err := time.Parse(time.RFC3339, f.Date)
	if err != nil || date.Unix() <= 0 {
		return TextError(ErrorInvalidReference, nil)
	}
	for _, label := range []string{f.FromName, f.PostAuthor} {
		if !utf8.ValidString(label) || len(label) > 4096 {
			return TextError(ErrorInvalidReference, nil)
		}
	}
	if f.FromPeer != "" {
		peer, err := ParsePeerID(f.FromPeer)
		if err != nil || peer.Kind() == PeerKindSelf || peer.TopicID() != 0 {
			return TextError(ErrorInvalidReference, nil)
		}
	}
	return nil
}

func SameForward(a, b *Forward) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
