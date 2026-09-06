package control

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

type topicController struct {
	fakeTextController
	calls    int
	position model.TopicPosition
}

func (c *topicController) Topics(_ context.Context, p model.PeerID, position model.TopicPosition) (model.TopicPage, error) {
	c.calls++
	c.position = position
	id, _ := model.NewTopicPeer(p, 7)
	return model.TopicPage{Items: []model.Topic{{ID: id, Title: "untrusted\x1b[2J"}}}, nil
}
func TestHumanTopicsValidatesOffsetsAndEscapesTitles(t *testing.T) {
	c := &topicController{}
	var out, stderr bytes.Buffer
	args := []string{"topics", "--peer", "tgpeer:v1:channel:42", "--offset-date", "100", "--offset-message", "20", "--offset-topic", "7"}
	if code := runTopicsCommand(context.Background(), args, &out, &stderr, c); code != 0 || c.calls != 1 || c.position.Topic != 7 || strings.Contains(out.String(), "\x1b") {
		t.Fatal(code, stderr.String())
	}
	for _, args := range [][]string{
		{"topics", "--peer", "tgpeer:v1:channel:42:topic:7"},
		{"topics", "--peer", "tgpeer:v1:channel:42", "--offset-date", "100"},
		{"topics", "--peer", "tgpeer:v1:channel:42", "--peer", "tgpeer:v1:channel:43"},
	} {
		if code := runTopicsCommand(context.Background(), args, &out, &stderr, c); code == 0 || c.calls != 1 {
			t.Fatal("invalid metadata request reached Telegram")
		}
	}
}
