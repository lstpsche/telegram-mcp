package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestFormattingPreservesContainingTextAndInertReferences(t *testing.T) {
	m := testMessage(10)
	m.Message = "A😀 code"
	m.Entities = []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 1, Length: 2}, &tg.MessageEntityPre{Offset: 4, Length: 4, Language: "go"}, &tg.MessageEntityTextURL{Offset: 0, Length: 1, URL: "https://example.invalid"}, &tg.MessageEntityBlockquote{Offset: 0, Length: 8, Collapsed: true}, &tg.MessageEntityMentionName{Offset: 0, Length: 1, UserID: 999}, &tg.MessageEntityCustomEmoji{Offset: 1, Length: 2, DocumentID: -9}}
	c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false)
	if err != nil || c.Quoted || c.Message.Text != m.Message || len(c.Message.Entities) != 6 || c.Message.Entities[4].User != "tgpeer:v1:user:999" || !c.Message.Entities[3].Collapsed {
		t.Fatal("formatting contract failed", err)
	}
	m.ReplyTo = &tg.MessageReplyHeader{Quote: true, QuoteText: "external body"}
	c, err = normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false)
	if err != nil || !c.Quoted || c.Message.Text != "" || len(c.Message.Entities) != 0 {
		t.Fatal("external quote released", err)
	}
}

func TestMalformedFormattingWithholdsMessage(t *testing.T) {
	var missing *tg.MessageEntityPre
	for _, entities := range [][]tg.MessageEntityClass{{&tg.MessageEntityBold{Offset: 2, Length: 1}}, {missing}, {&tg.MessageEntityTextURL{Offset: 0, Length: 1, URL: ""}}, {&tg.InputMessageEntityMentionName{Offset: 0, Length: 1}}, {&tg.MessageEntityDiffReplace{Offset: 0, Length: 1, OldText: "unavailable"}}} {
		m := testMessage(10)
		m.Message = "A😀"
		m.Entities = entities
		c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false)
		if model.TextErrorCategory(err) != model.ErrorInvalidReference || c.Message.Text != "" {
			t.Fatal("malformed entity released", err)
		}
	}
}

func TestFormattedDateMetadataStaysInert(t *testing.T) {
	entities, err := normalizeTextEntities("today", []tg.MessageEntityClass{&tg.MessageEntityFormattedDate{Offset: 0, Length: 5, Date: 1788609600, LongDate: true, ShortTime: true}})
	if err != nil || len(entities) != 1 || entities[0].Date == nil || *entities[0].Date != 1788609600 || len(entities[0].DateFormat) != 2 {
		t.Fatal("date metadata lost", err)
	}
	_, err = normalizeTextEntities("today", []tg.MessageEntityClass{&tg.MessageEntityFormattedDate{Offset: 0, Length: 5, Date: 1788609600, Relative: true, LongDate: true}})
	if err == nil {
		t.Fatal("incompatible date flags accepted")
	}
}
