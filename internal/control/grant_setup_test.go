package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func TestGuidedSavedGrantRequiresExactFinalConsent(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, accept := range []bool{true, false} {
		final := "no"
		if accept {
			final = "yes"
		}
		control := &fakeTextController{}
		s := setupSession{control: control, prompt: &setupAnswers{answers: []string{"saved", "yes", "24", "0", "no", final}}, output: io.Discard}
		err := s.guideGrant(context.Background(), now)
		if !accept {
			if !errors.Is(err, context.Canceled) || control.saved.Eligible {
				t.Fatal("refusal saved authority", err)
			}
			continue
		}
		g := control.saved
		if err != nil || g.Peer.String() != "tgpeer:v1:self:456" || g.Author.String() != "tgpeer:v1:user:456" || g.MinID != 120 || g.MaxID != 120 || g.ReadThrough != 0 || g.Images || g.Profile != policy.ProfileSelfAuthored || !g.ExpiresAt.Equal(now.Add(24*time.Hour)) {
			t.Fatal(g, err)
		}
	}
}
func TestGuidedChatUsesNumberedImmutableIDAndEscapesTitle(t *testing.T) {
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	title := "untrusted\x1b[2J\u202e\U000e0001"
	control := &fakeTextController{peers: []model.Chat{{ID: peer, Title: title}}}
	var out bytes.Buffer
	s := setupSession{control: control, prompt: &setupAnswers{answers: []string{"chats", "yes", "1", "tgpeer:v1:user:456", "consented", "100", "tgmsg:v1:chat:123:120", "2", "120", "yes", "yes"}}, output: &out}
	if err := s.guideGrant(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if control.saved.Peer != peer || control.saved.MinID != 100 || control.saved.ReadThrough != 120 || !control.saved.Images {
		t.Fatal(control.saved)
	}
	if strings.ContainsAny(out.String(), "\x1b\u202e\U000e0001") {
		t.Fatal("raw terminal controls")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, `{"number"`) {
			var value struct {
				Chat model.Chat `json:"chat"`
			}
			if err := json.Unmarshal([]byte(line), &value); err != nil || value.Chat.Title != title {
				t.Fatal("escaped metadata changed", err)
			}
		}
	}
}
func TestGuidedGrantRejectsCrossPeerMessageAndInvalidLifetime(t *testing.T) {
	for _, answers := range [][]string{
		{"id", "tgpeer:v1:chat:123", "tgpeer:v1:user:456", "consented", "tgmsg:v1:chat:999:100"},
		{"saved", "yes", "721"},
		{"saved", "yes", "0"},
		{"saved", "no"},
	} {
		control := &fakeTextController{}
		s := setupSession{control: control, prompt: &setupAnswers{answers: answers}, output: io.Discard}
		if err := s.guideGrant(context.Background(), time.Now()); err == nil || control.saved.Eligible {
			t.Fatal("invalid or declined grant saved")
		}
	}
}

type replacementControl struct {
	fakeTextController
	current policy.Grant
}

func (c *replacementControl) Grants(context.Context) ([]policy.Grant, error) {
	return []policy.Grant{c.current}, nil
}
func TestGuidedReplacementCanBeDeclined(t *testing.T) {
	peer, _ := model.ParsePeerID("tgpeer:v1:self:456")
	author, _ := model.ParsePeerID("tgpeer:v1:user:456")
	c := &replacementControl{current: policy.Grant{Peer: peer, Author: author}}
	s := setupSession{control: c, prompt: &setupAnswers{answers: []string{"saved", "yes", "24", "0", "no", "no"}}, output: io.Discard}
	if err := s.guideGrant(context.Background(), time.Now()); !errors.Is(err, context.Canceled) || c.saved.Eligible {
		t.Fatal("replacement not refused", err)
	}
}
