package reader

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestTextFormatsRejectInvalidEncodingBeforeReceipt(t *testing.T) {
	for _, mime := range []string{"text/markdown", "text/csv", "application/json"} {
		for _, invalid := range []byte{0xff, 0, 0x1b} {
			s, f, _, _, token := documentService(t, mime)
			f.data[0] = invalid
			result, err := s.OpenDocument(context.Background(), "req_invalid_text", token)
			if model.TextErrorCategory(err) != model.ErrorInvalidReference || result.Document != nil || len(result.JSON) != 0 || f.ackCalls != 0 || !bytes.Equal(f.data, make([]byte, len(f.data))) {
				t.Fatal("invalid text released or retained", err)
			}
		}
		source := model.MediaSource{Kind: "document", MIMEType: mime, Size: model.MaximumTextAttachmentBytes + 1, Fingerprint: strings.Repeat("a", 64)}
		if model.TextErrorCategory(source.Validate()) != model.ErrorMediaTooLarge {
			t.Fatal("text size limit missing")
		}
		data := []byte("\xef\xbb\xbf# text\r\n=1+1\t{invalid JSON}\n")
		source.Size = int64(len(data))
		if err := validateDocumentData(source, data); err != nil {
			t.Fatal("valid original text rejected", err)
		}
	}
}
