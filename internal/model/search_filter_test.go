package model

import (
	"strings"
	"testing"
)

func TestSearchFilterValidation(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, query := range []string{"", " \t ", " q ", strings.Repeat("я", 256), strings.Repeat("я", 257), strings.Repeat(" ", 1025), string([]byte{0xff})} {
			filter, err := (SearchFilter{Query: query, PinnedOnly: pinned}).Normalize()
			valid := (query == " q " || query == strings.Repeat("я", 256) || (pinned && (query == "" || query == " \t ")))
			if (err == nil) != valid {
				t.Fatal("wrong search filter validation", pinned, len(query), err)
			}
			if valid && (filter.Query != strings.TrimSpace(query) || filter.PinnedOnly != pinned) {
				t.Fatal("search filter changed semantics")
			}
		}
	}
}
