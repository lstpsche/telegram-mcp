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

func TestSearchMediaTypeValidation(t *testing.T) {
	for _, kind := range []SearchMediaType{SearchMediaPhoto, SearchMediaImageFile, SearchMediaPDF, SearchMediaTextFile, SearchMediaVoiceNote} {
		for _, q := range []string{"", " \t ", " caption "} {
			f, err := (SearchFilter{MediaType: kind, Query: q}).Normalize()
			if err != nil || f.MediaType != kind || f.Query != strings.TrimSpace(q) {
				t.Fatal("valid media filter rejected", err)
			}
		}
	}
	for _, kind := range []SearchMediaType{"image", "document", "video", "PDF", " pdf"} {
		_, err := (SearchFilter{MediaType: kind, Query: "caption", PinnedOnly: true}).Normalize()
		if TextErrorCategory(err) != ErrorInvalidInput {
			t.Fatal("unknown media type accepted")
		}
	}
	for _, q := range []string{strings.Repeat("a", 257), strings.Repeat(" ", 1025), string([]byte{0xff})} {
		if _, err := (SearchFilter{MediaType: SearchMediaPDF, Query: q}).Normalize(); TextErrorCategory(err) != ErrorInvalidInput {
			t.Fatal("media filter bypassed query limits")
		}
	}
}

func TestSenderDateFilters(t *testing.T) {
	for _, f := range []SearchFilter{{Sender: "tgpeer:v1:user:1"}, {Sender: "tgpeer:v1:channel:1"}, {Since: 1}, {Until: 2}, {Since: 1, Until: 2}} {
		if _, err := f.Normalize(); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []SearchFilter{{Sender: "name"}, {Sender: "tgpeer:v1:self:1"}, {Sender: "tgpeer:v1:chat:1"}, {Sender: "tgpeer:v1:channel:1:topic:1"}, {Since: -1}, {Until: -1}, {Since: 2, Until: 2}, {Since: 3, Until: 2}, {Until: 2147483648}} {
		if _, err := f.Normalize(); TextErrorCategory(err) != ErrorInvalidInput {
			t.Fatal("invalid filter accepted")
		}
	}
	for _, date := range []string{"", "2026-09-07", "2026-09-07T12:00:00", "2026-09-07T12:00:00.1Z", "1970-01-01T00:00:00Z", "2040-01-01T00:00:00Z"} {
		if _, err := ParseSearchDate(date); err == nil {
			t.Fatal("invalid date accepted")
		}
	}
	a, err := ParseSearchDate("2026-09-07T12:00:00+03:00")
	b, _ := ParseSearchDate("2026-09-07T09:00:00Z")
	if err != nil || a != b {
		t.Fatal("timezone normalization failed")
	}
}
