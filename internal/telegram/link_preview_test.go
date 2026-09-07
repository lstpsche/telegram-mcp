package telegram

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestLinkPreviewPreservesMessageAndIgnoresEmbeddedMedia(t *testing.T) {
	page := &tg.WebPage{ID: 1, URL: "https://preview.invalid", DisplayURL: "misleading display"}
	page.SetTitle("Preview title")
	page.SetSiteName("Site")
	page.SetDescription("Description")
	page.SetPhoto(&tg.Photo{ID: 42, AccessHash: 123})
	page.SetEmbedURL("https://embed.invalid")
	message := testPhotoMessage()
	message.Message = "Original https://message.invalid"
	message.Media = &tg.MessageMediaWebPage{Webpage: page, Manual: true, Safe: true}
	c, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Message.LinkPreview
	if c.Unsupported || p == nil || p.State != "available" || !p.Manual || p.URL != page.URL || p.Title != "Preview title" || p.SiteName != "Site" || p.Description != "Description" || c.Message.Text != message.Message {
		t.Fatal("preview or original message lost")
	}
	if c.Image != nil || c.Document != nil || c.Voice != nil {
		t.Fatal("preview created media authority")
	}
}

func TestLinkPreviewAvailabilityAndExclusions(t *testing.T) {
	for _, kind := range []string{"available", "pending", "unavailable", "standalone", "protected", "paid", "ephemeral", "quoted", "album"} {
		t.Run(kind, func(t *testing.T) {
			message := testPhotoMessage()
			message.Message = "Original message"
			page := &tg.WebPage{ID: 1, URL: "https://example.invalid", Title: "absent title"}
			media := &tg.MessageMediaWebPage{Webpage: page}
			message.Media = media
			state := "available"
			excluded := false
			switch kind {
			case "pending":
				media.Webpage = &tg.WebPagePending{ID: 1, Date: 1}
				state = "pending"
			case "unavailable":
				media.Webpage = &tg.WebPageEmpty{}
				state = "unavailable"
			case "standalone":
				message.Message = ""
			case "protected":
				message.Noforwards = true
				excluded = true
			case "paid":
				message.PaidMessageStars = 1
				excluded = true
			case "ephemeral":
				message.TTLPeriod = 1
				excluded = true
			case "quoted":
				message.ReplyTo = &tg.MessageReplyHeader{Quote: true, QuoteText: "external quote"}
				excluded = true
			case "album":
				message.GroupedID = 1
				excluded = true
			}
			c, err := normalizeMessage(testSelfPeer(t), 1, message, map[int64]bool{1: true}, false)
			if err != nil {
				t.Fatal(err)
			}
			if excluded {
				if c.Message.LinkPreview != nil || c.Message.Text != "" {
					t.Fatal("excluded body escaped")
				}
			} else if c.Unsupported || c.Message.LinkPreview == nil || c.Message.LinkPreview.State != state || c.Message.LinkPreview.Title != "" || c.Message.Text != message.Message {
				t.Fatal("message or presence lost")
			}
		})
	}
}

func TestMalformedLinkPreviewFailsClosed(t *testing.T) {
	for _, page := range []tg.WebPageClass{nil, (*tg.WebPage)(nil), &tg.WebPageNotModified{}, &tg.WebPage{ID: 1}, &tg.WebPage{URL: "https://example.invalid"}, &tg.WebPage{ID: 1, URL: strings.Repeat("x", 4097)}, &tg.WebPage{ID: 1, URL: string([]byte{255})}, &tg.WebPagePending{ID: 1}} {
		if _, err := normalizeLinkPreview(&tg.MessageMediaWebPage{Webpage: page}); err == nil {
			t.Fatal("invalid preview accepted")
		}
	}
}
