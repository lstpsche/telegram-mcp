package telegram

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestReactionSnapshotsPreserveCountsAndOmitPeople(t *testing.T) {
	peer, _ := model.NewPeerID(model.PeerKindChat, 42)
	supplied := tg.MessageReactions{Min: true, CanSeeList: true, Results: []tg.ReactionCount{
		{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 0},
		{Reaction: &tg.ReactionCustomEmoji{DocumentID: math.MaxInt64}, Count: 2},
		{Reaction: &tg.ReactionPaid{}, Count: 9},
	}}
	supplied.Results[0].SetChosenOrder(123)
	supplied.SetRecentReactions([]tg.MessagePeerReaction{{PeerID: &tg.PeerUser{UserID: 9876543}}})
	supplied.SetTopReactors([]tg.MessageReactor{{Count: 8765432}})
	result, err := normalizeReactions(peer, supplied)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Minimal || result.AsTags || len(result.Counts) != 3 || result.Counts[0].Count != 0 || result.Counts[1].CustomEmojiID != "9223372036854775807" || result.Counts[2].Kind != "paid" || result.Counts[2].Count != 9 {
		t.Fatal("aggregate semantics lost")
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"9876543", "8765432", "chosen", "recent", "peer", "can_see"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("personal metadata escaped")
		}
	}
}

func TestReactionPresenceTagsAndMessageExclusions(t *testing.T) {
	for _, kind := range []string{"absent", "empty", "legacy", "tags", "protected", "ephemeral", "quoted", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			message := testPhotoMessage()
			message.Media = nil
			message.Message = "Original"
			supplied := tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}}
			if kind == "empty" {
				supplied.Results = nil
			}
			if kind == "tags" {
				supplied.ReactionsAsTags = true
			}
			if kind == "absent" {
				message.Reactions = supplied
			} else {
				message.SetReactions(supplied)
			}
			excluded := false
			switch kind {
			case "protected":
				message.Noforwards = true
				excluded = true
			case "ephemeral":
				message.TTLPeriod = 1
				excluded = true
			case "quoted":
				message.ReplyTo = &tg.MessageReplyHeader{Quote: true, QuoteText: "external quote"}
				excluded = true
			case "unsupported":
				message.Media = &tg.MessageMediaContact{}
				excluded = true
			}
			c, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true}, false)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "absent" || excluded {
				if c.Message.Reactions != nil {
					t.Fatal("absent or excluded metadata escaped")
				}
			} else {
				r := c.Message.Reactions
				if r == nil || r.AsTags != (kind == "tags" || kind == "empty") || r.Counts == nil || c.Message.Text != "Original" {
					t.Fatal("presence or tag semantics lost")
				}
				if kind == "empty" && len(r.Counts) != 0 {
					t.Fatal("empty reactions fabricated")
				}
			}
		})
	}
}

func TestMalformedReactionsFailClosed(t *testing.T) {
	peer, _ := model.NewPeerID(model.PeerKindChat, 42)
	for _, kind := range []string{"nil", "typed_nil", "empty", "duplicate", "negative", "overflow", "custom_zero", "oversized", "utf8", "too_many", "tags_outside_self"} {
		t.Run(kind, func(t *testing.T) {
			value := tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}}
			switch kind {
			case "nil":
				value.Results[0].Reaction = nil
			case "typed_nil":
				value.Results[0].Reaction = (*tg.ReactionEmoji)(nil)
			case "empty":
				value.Results[0].Reaction = &tg.ReactionEmpty{}
			case "duplicate":
				value.Results = append(value.Results, value.Results[0])
			case "negative":
				value.Results[0].Count = -1
			case "overflow":
				value.Results[0].Count = 2147483648
			case "custom_zero":
				value.Results[0].Reaction = &tg.ReactionCustomEmoji{}
			case "oversized":
				value.Results[0].Reaction = &tg.ReactionEmoji{Emoticon: strings.Repeat("x", 129)}
			case "utf8":
				value.Results[0].Reaction = &tg.ReactionEmoji{Emoticon: string([]byte{255})}
			case "too_many":
				value.Results = make([]tg.ReactionCount, 101)
			case "tags_outside_self":
				value.ReactionsAsTags = true
			}
			if r, err := normalizeReactions(peer, value); err == nil || r != nil {
				t.Fatal("malformed aggregate accepted")
			}
		})
	}
}
