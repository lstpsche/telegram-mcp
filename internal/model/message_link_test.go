package model

import "testing"

func TestMessageLinkParsingAndCanonicalURL(t *testing.T) {
	for _, raw := range []string{"https://t.me/c/42/7/20", "https://telegram.me/c/42/20?thread=7&single", "https://telegram.dog/c/42/7/20?single"} {
		link, err := ParseMessageLink(raw)
		if err != nil || link.Channel != 42 || link.Topic != 7 || link.Message != 20 || link.Username != "" {
			t.Fatal(raw, link, err)
		}
	}
	link, err := ParseMessageLink("https://t.me/Synthetic_chat/7/20")
	if err != nil || link.Username != "Synthetic_chat" || link.Topic != 7 {
		t.Fatal(link, err)
	}
	for _, raw := range []string{"https://evil.test/c/42/20", "https://t.me.evil.test/c/42/20", "https://t.me@evil.test/c/42/20", "https://u@t.me/c/42/20", "http://t.me/c/42/20", "https://t.me:443/c/42/20", "https://t.me/c/42/0", "https://t.me/c/042/20", "https://t.me/c/42/2147483648", "https://t.me/c/42/20?comment=1", "https://t.me/c/42/7/20?thread=7", "https://t.me/c/42/20?thread=7&thread=8", "https://t.me/c/42/20?single=1", "https://t.me/c/42/20#x", "https://t.me/c/42/%32%30", "https://t.me/c/42/21/20", "https://t.me/joinchat/20", "https://t.me/c/42/20/", "https://t.me/c/42/20?thread=-1", "https://t.me/c/42/20?thread=%ZZ"} {
		if _, err := ParseMessageLink(raw); err == nil {
			t.Fatal("invalid link accepted", raw)
		}
	}
	id, _ := ParseMessageID("tgmsg:v1:channel:42:topic:7:20")
	if id.URL() != "https://t.me/c/42/7/20?single" {
		t.Fatal(id.URL())
	}
	id, _ = ParseMessageID("tgmsg:v1:user:42:20")
	if id.URL() != "" {
		t.Fatal("private user link fabricated")
	}
}
