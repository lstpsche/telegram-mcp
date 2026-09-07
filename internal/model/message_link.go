package model

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var linkUsername = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

// MessageLink is transient navigation input, never authority or stored metadata.
// Topic identifies an explicit forum topic, not an arbitrary reply thread.
type MessageLink struct {
	Channel  int64
	Username string
	Message  int32
	Topic    int32
}

func ParseMessageLink(value string) (MessageLink, error) {
	invalid := func() (MessageLink, error) { return MessageLink{}, TextError(ErrorInvalidInput, nil) }
	if len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\r\n\t ") {
		return invalid()
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" {
		return invalid()
	}
	switch u.Host {
	case "t.me", "telegram.me", "telegram.dog":
	default:
		return invalid()
	}
	if strings.Contains(u.EscapedPath(), "%") {
		return invalid()
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	link := MessageLink{}
	if len(parts) < 2 {
		return invalid()
	}
	if parts[0] == "c" {
		if len(parts) != 3 && len(parts) != 4 {
			return invalid()
		}
		n, err := parseCanonicalPositive(parts[1], uint64(^uint64(0)>>1))
		if err != nil {
			return invalid()
		}
		link.Channel = int64(n)
		parts = parts[2:]
	} else {
		if len(parts) != 2 && len(parts) != 3 || !linkUsername.MatchString(parts[0]) {
			return invalid()
		}
		// These paths invoke Telegram actions rather than identify publishers.
		switch strings.ToLower(parts[0]) {
		case "joinchat", "addlist", "share", "proxy", "socks", "login", "confirmphone", "setlanguage", "addstickers", "addemoji", "addtheme", "invoice", "boost", "contact", "giftcode", "m", "web", "a", "k", "z":
			return invalid()
		}
		link.Username = parts[0]
		parts = parts[1:]
	}
	positive := func(v string) (int32, error) { n, err := parseCanonicalPositive(v, 2147483647); return int32(n), err }
	if len(parts) == 2 {
		link.Topic, err = positive(parts[0])
		if err != nil {
			return invalid()
		}
		parts = parts[1:]
	}
	link.Message, err = positive(parts[0])
	if err != nil {
		return invalid()
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return invalid()
	}
	for key, values := range query {
		if len(values) != 1 {
			return invalid()
		}
		switch key {
		case "single":
			if values[0] != "" {
				return invalid()
			}
		case "thread":
			if link.Topic != 0 {
				return invalid()
			}
			link.Topic, err = positive(values[0])
			if err != nil {
				return invalid()
			}
		default:
			return invalid()
		}
	}
	if link.Topic > link.Message {
		return invalid()
	}
	return link, nil
}

// URL produces a stable numeric link for supported channel/topic addresses.
// Telegram does not define these links for private users or basic groups.
func (id MessageID) URL() string {
	if !id.valid() || id.Peer().Kind() != PeerKindChannel {
		return ""
	}
	path := "https://t.me/c/" + strconv.FormatInt(id.Peer().TelegramID(), 10) + "/"
	if id.Peer().TopicID() != 0 {
		path += strconv.Itoa(int(id.Peer().TopicID())) + "/"
	}
	return path + strconv.Itoa(int(id.TelegramID())) + "?single"
}
