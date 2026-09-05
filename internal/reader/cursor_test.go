package reader

import (
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestCursorCanonicalBindingAndReplay(t *testing.T) {
	s, _, _, _, g := testService(t)
	binding := cursorBinding{Operation: "search_messages", Peer: g.Peer, QueryDigest: s.queryDigest("private query"), Limit: 20, Epoch: strings.Repeat("e", 43), Revision: 1}
	cursor := searchCursor{Binding: binding, Before: 15, Ceiling: 20, Expires: s.now().Add(time.Minute).Unix()}
	token, err := s.encodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := s.decodeCursor(token, binding)
		if err != nil || got != cursor {
			t.Fatal("valid replay failed", err)
		}
	}
	for _, mutate := range []func(*cursorBinding){func(b *cursorBinding) { b.Operation = "list_messages" }, func(b *cursorBinding) { b.Revision++ }, func(b *cursorBinding) { b.Epoch = "other" }} {
		changed := binding
		mutate(&changed)
		if _, err := s.decodeCursor(token, changed); model.TextErrorCategory(err) != model.ErrorCursorInvalid {
			t.Fatal("binding mismatch accepted")
		}
	}
	for _, bad := range []string{"", token + "=", token + ".extra", strings.Repeat("x", 4097), "sc2" + token[3:]} {
		if _, err := s.decodeCursor(bad, binding); model.TextErrorCategory(err) != model.ErrorCursorInvalid {
			t.Fatal("invalid encoding accepted")
		}
	}
	cursor.Expires = s.now().Unix()
	token, err = s.encodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.decodeCursor(token, binding); model.TextErrorCategory(err) != model.ErrorCursorExpired {
		t.Fatal("expired cursor accepted")
	}
	cursor.Expires = s.now().Add(cursorLifetime + time.Second).Unix()
	token, err = s.encodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.decodeCursor(token, binding); model.TextErrorCategory(err) != model.ErrorCursorInvalid {
		t.Fatal("excessive lifetime accepted")
	}
}
