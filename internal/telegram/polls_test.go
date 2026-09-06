package telegram

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func testPollMedia() *tg.MessageMediaPoll {
	return &tg.MessageMediaPoll{Poll: tg.Poll{ID: 1, Question: tg.TextWithEntities{Text: "Which option?"}, Answers: []tg.PollAnswerClass{
		&tg.PollAnswer{Text: tg.TextWithEntities{Text: "First"}, Option: []byte{1}},
		&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Second"}, Option: []byte{2}},
	}}}
}
func TestPollCountsPreservePresenceAndMatchOptions(t *testing.T) {
	media := testPollMedia()
	media.Results.Min = true
	media.Results.SetTotalVoters(0)
	count := tg.PollAnswerVoters{Option: []byte{2}, Chosen: true}
	count.SetVoters(0)
	media.Results.SetResults([]tg.PollAnswerVoters{count})
	media.Results.SetRecentVoters([]tg.PeerClass{&tg.PeerUser{UserID: 1234567}})
	media.Results.SetSolution("private quiz solution")
	poll, err := normalizePoll(media)
	if err != nil || poll == nil || !poll.Minimal || poll.TotalVoters == nil || *poll.TotalVoters != 0 || poll.Options[0].Voters != nil || poll.Options[1].Voters == nil || *poll.Options[1].Voters != 0 {
		t.Fatal("count presence lost", err)
	}
	data, _ := json.Marshal(poll)
	for _, secret := range []string{"1234567", "private quiz solution", "chosen", "option\""} {
		if strings.Contains(string(data), secret) {
			t.Fatal("private poll metadata escaped")
		}
	}
}
func TestPollNormalizationAndExclusions(t *testing.T) {
	for _, kind := range []string{"ordinary", "protected", "paid", "ephemeral", "album", "media_answer", "attached_media", "duplicate", "unknown_result", "negative", "oversized", "utf8"} {
		t.Run(kind, func(t *testing.T) {
			media := testPollMedia()
			message := testPhotoMessage()
			message.Media = media
			message.Message = ""
			malformed := false
			excluded := false
			switch kind {
			case "protected":
				message.Noforwards = true
				excluded = true
			case "paid":
				message.PaidMessageStars = 1
				excluded = true
			case "ephemeral":
				message.TTLPeriod = 10
				excluded = true
			case "album":
				message.GroupedID = 1
				excluded = true
			case "media_answer":
				media.Poll.Answers[0].(*tg.PollAnswer).Media = &tg.MessageMediaPhoto{}
				excluded = true
			case "attached_media":
				media.AttachedMedia = &tg.MessageMediaPhoto{}
				excluded = true
			case "duplicate":
				media.Poll.Answers[1] = media.Poll.Answers[0]
				malformed = true
			case "unknown_result":
				media.Results.SetResults([]tg.PollAnswerVoters{{Option: []byte{3}}})
				malformed = true
			case "negative":
				media.Results.SetTotalVoters(-1)
				malformed = true
			case "oversized":
				media.Poll.Question.Text = strings.Repeat("x", 4097)
				malformed = true
			case "utf8":
				media.Poll.Question.Text = string([]byte{255})
				malformed = true
			}
			c, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true}, false)
			if malformed {
				if err == nil {
					t.Fatal("malformed poll accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if excluded {
				if c.Message.Poll != nil {
					t.Fatal("excluded poll escaped")
				}
				return
			}
			if c.Unsupported || c.Message.Poll == nil || c.Message.Poll.Question != "Which option?" {
				t.Fatal("poll lost")
			}
		})
	}
}
