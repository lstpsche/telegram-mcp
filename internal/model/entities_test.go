package model

import (
	"math"
	"testing"
)

func TestTextEntitiesValidateUTF16BoundariesWithoutChangingText(t *testing.T) {
	text := "A😀 code"
	entities := []TextEntity{{Kind: "bold", Offset: 1, Length: 2}, {Kind: "pre", Offset: 4, Length: 4, Language: "go"}, {Kind: "text_url", Offset: 0, Length: 3, URL: "https://example.invalid"}}
	if err := ValidateTextEntities(text, entities); err != nil {
		t.Fatal(err)
	}
	for _, e := range []TextEntity{{Kind: "bold", Offset: 2, Length: 1}, {Kind: "italic", Offset: 1, Length: 1}, {Kind: "code", Offset: -1, Length: 1}, {Kind: "bold", Offset: 1, Length: math.MaxInt}, {Kind: "bold", Offset: 0, Length: 0}, {Kind: "bold", Offset: 0, Length: 1, URL: "https://example.invalid"}, {Kind: "text_url", Offset: 0, Length: 1, URL: "bad\nurl"}, {Kind: "unknown_type", Offset: 0, Length: 1}} {
		if err := ValidateTextEntities(text, []TextEntity{e}); err == nil {
			t.Fatal("invalid entity accepted")
		}
	}
	if err := ValidateTextEntities(text, make([]TextEntity, MaximumTextEntities+1)); TextErrorCategory(err) != ErrorResultTooLarge {
		t.Fatal("unbounded entities accepted", err)
	}
}
