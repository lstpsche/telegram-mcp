package model

// SavedTag selects one existing reaction tag, never an ordinary reaction or a write.
type SavedTag struct {
	Kind          string `json:"kind"`
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

func (tag SavedTag) Valid() bool {
	self, _ := NewPeerID(PeerKindSelf, 1)
	return (&Reactions{AsTags: true, Counts: []ReactionCount{{Kind: tag.Kind, Emoji: tag.Emoji, CustomEmojiID: tag.CustomEmojiID, Count: 1}}}).Valid(self)
}

func (tag SavedTag) Matches(reactions *Reactions) bool {
	if reactions == nil || !reactions.AsTags {
		return false
	}
	for _, count := range reactions.Counts {
		if count.Count > 0 && count.Kind == tag.Kind && count.Emoji == tag.Emoji && count.CustomEmojiID == tag.CustomEmojiID {
			return true
		}
	}
	return false
}

// SavedPeer is provider-supplied grouping metadata, never origin authority.
func (m Message) ValidSavedPeer() bool {
	if m.SavedPeer == "" {
		return true
	}
	peer, err := ParsePeerID(m.SavedPeer)
	return err == nil && m.ID.Peer().Kind() == PeerKindSelf && peer.TopicID() == 0 && (peer.Kind() != PeerKindSelf || peer == m.ID.Peer()) && (peer.Kind() != PeerKindUser || peer.TelegramID() != m.ID.Peer().TelegramID())
}

func (f SearchFilter) HasSavedFilter() bool {
	return f.SavedPeer != "" || f.SavedTag != (SavedTag{})
}
