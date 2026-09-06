package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func voiceMessage() *tg.Message {
	m := attachmentMessage("audio/ogg")
	d := m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
	d.Attributes = append(d.Attributes, &tg.DocumentAttributeAudio{Voice: true, Duration: 1})
	return m
}
func TestVoiceNormalizationKeepsAudioDistinctFromDocuments(t *testing.T) {
	m := voiceMessage()
	m.Mentioned, m.MediaUnread = true, true
	c := imageCandidate(t, m)
	if c.Voice == nil || c.Document != nil || c.Image != nil || c.Message.Date == "" || c.Voice.Duration != 1 {
		t.Fatal("voice was not normalized independently")
	}
}
func TestVoiceNormalizationRejectsUnsafeAudio(t *testing.T) {
	for name, mutate := range map[string]func(*tg.Message, *tg.Document, *tg.DocumentAttributeAudio){
		"music":    func(_ *tg.Message, _ *tg.Document, a *tg.DocumentAttributeAudio) { a.Voice = false },
		"duration": func(_ *tg.Message, _ *tg.Document, a *tg.DocumentAttributeAudio) { a.Duration = 301 },
		"format":   func(_ *tg.Message, d *tg.Document, _ *tg.DocumentAttributeAudio) { d.MimeType = "audio/mpeg" },
		"size": func(_ *tg.Message, d *tg.Document, _ *tg.DocumentAttributeAudio) {
			d.Size = model.MaximumVoiceBytes + 1
		},
		"protected": func(m *tg.Message, _ *tg.Document, _ *tg.DocumentAttributeAudio) { m.Noforwards = true },
		"ephemeral": func(m *tg.Message, _ *tg.Document, _ *tg.DocumentAttributeAudio) { m.TTLPeriod = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			m := voiceMessage()
			d := m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
			mutate(m, d, d.Attributes[1].(*tg.DocumentAttributeAudio))
			c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true})
			if err != nil || c.Voice != nil || c.Message.Text != "" {
				t.Fatal("unsafe audio escaped", err)
			}
		})
	}
}
