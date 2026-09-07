package telegram

import (
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestEditTimestampNormalizesOnlyValidCurrentMetadata(t *testing.T) {
	m := testMessage(10)
	m.SetEditDate(101)
	c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false)
	if err != nil || c.Message.EditedAt != "1970-01-01T00:01:41Z" {
		t.Fatal("edit timestamp missing", err)
	}
	m.SetEditDate(99)
	c, err = normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false)
	if model.TextErrorCategory(err) != model.ErrorInvalidReference || c.Message.Text != "" {
		t.Fatal("regressing edit accepted", err)
	}
}
