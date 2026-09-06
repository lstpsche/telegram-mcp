package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestAlbumNormalizationKeepsMemberCaptionsAndExclusions(t *testing.T) {
	for _, scenario := range []string{"caption", "captionless", "negative_id", "protected", "unsupported", "text_only", "present_zero"} {
		t.Run(scenario, func(t *testing.T) {
			m := testPhotoMessage()
			m.GroupedID = 9007199254740993
			safe := true
			switch scenario {
			case "captionless":
				m.Message = ""
			case "negative_id":
				m.GroupedID = -1
			case "protected":
				m.Noforwards = true
				safe = false
			case "unsupported":
				m.Media = &tg.MessageMediaUnsupported{}
				safe = false
			case "text_only":
				m.Media = nil
				safe = false
			case "present_zero":
				m.GroupedID = 0
				m.Flags.Set(17)
				safe = false
			}
			c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false)
			if err != nil {
				t.Fatal(err)
			}
			if safe {
				expected, _ := model.NewAlbumID(testSelfPeer(t), m.GroupedID)
				if c.Message.AlbumID != expected || c.Message.Text != m.Message || c.Image == nil {
					t.Fatal("album member lost")
				}
			} else if c.Message.AlbumID != "" || c.Message.Text != "" || c.Image != nil {
				t.Fatal("unsafe album escaped")
			}
		})
	}
}
