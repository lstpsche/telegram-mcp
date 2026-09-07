package model

import (
	"strings"
	"testing"
)

func TestAttachmentFilenameBoundsAndSearchNormalization(t *testing.T) {
	for _, name := range []string{"plain.pdf", "../../untrusted.exe", strings.Repeat("界", 256), ""} {
		if !ValidAttachmentFilename(name) {
			t.Fatal("valid display name rejected")
		}
	}
	for _, name := range []string{"bad\nname", string([]byte{255}), strings.Repeat("a", 257)} {
		if ValidAttachmentFilename(name) {
			t.Fatal("invalid display name accepted")
		}
		if _, err := (SearchFilter{FilenameQuery: name}).Normalize(); err == nil {
			t.Fatal("invalid filename query accepted")
		}
	}
	f, err := (SearchFilter{FilenameQuery: "  ОтЧЁТ.PDF  "}).Normalize()
	if err != nil || f.FilenameQuery != "отчёт.pdf" {
		t.Fatal("filename normalization failed", err)
	}
	if _, err := (SearchFilter{FilenameQuery: "   ", Query: "text"}).Normalize(); err == nil {
		t.Fatal("empty provided filename selector accepted")
	}
}
