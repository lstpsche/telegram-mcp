package model

import "testing"

func TestSavedTagValidationAndMatching(t *testing.T) {
	for _, tag := range []SavedTag{{Kind: "emoji", Emoji: "📌"}, {Kind: "custom_emoji", CustomEmojiID: "9223372036854775807"}} {
		if !tag.Valid() {
			t.Fatal("valid tag rejected")
		}
		count := ReactionCount{Kind: tag.Kind, Emoji: tag.Emoji, CustomEmojiID: tag.CustomEmojiID, Count: 1}
		r := &Reactions{AsTags: true, Counts: []ReactionCount{count}}
		if !tag.Matches(r) {
			t.Fatal("tag not matched")
		}
		r.AsTags = false
		if tag.Matches(r) {
			t.Fatal("ordinary reaction matched")
		}
		r.AsTags = true
		r.Counts[0].Count = 0
		if tag.Matches(r) {
			t.Fatal("zero count matched")
		}
		if tag.Matches(nil) {
			t.Fatal("absent reactions matched")
		}
	}
	for _, tag := range []SavedTag{{}, {Kind: "paid"}, {Kind: "emoji"}, {Kind: "emoji", Emoji: "📌", CustomEmojiID: "1"}, {Kind: "custom_emoji", CustomEmojiID: "0"}, {Kind: "custom_emoji", CustomEmojiID: "01"}, {Kind: "custom_emoji", CustomEmojiID: "9223372036854775808"}} {
		if tag.Valid() {
			t.Fatal("invalid tag accepted")
		}
	}
}

func TestSavedPeerMetadataValidation(t *testing.T) {
	self, _ := NewPeerID(PeerKindSelf, 7)
	id, _ := NewMessageID(self, 20)
	for _, source := range []string{"", self.String(), "tgpeer:v1:user:8", "tgpeer:v1:user:2666000", "tgpeer:v1:chat:8", "tgpeer:v1:channel:8"} {
		if !(Message{ID: id, SavedPeer: source}).ValidSavedPeer() {
			t.Fatal("valid saved grouping rejected")
		}
	}
	for _, source := range []string{"tgpeer:v1:self:8", "tgpeer:v1:user:7", "tgpeer:v1:channel:8:topic:1", "name"} {
		if (Message{ID: id, SavedPeer: source}).ValidSavedPeer() {
			t.Fatal("invalid saved grouping accepted")
		}
	}
	chat, _ := NewPeerID(PeerKindChat, 8)
	id, _ = NewMessageID(chat, 20)
	if (Message{ID: id, SavedPeer: self.String()}).ValidSavedPeer() {
		t.Fatal("non-saved grouping accepted")
	}
}
