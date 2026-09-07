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
