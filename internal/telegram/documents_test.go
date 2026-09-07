package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func attachmentMessage(mime string) *tg.Message {
	m := testDocumentMessage()
	d := m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
	d.MimeType = mime
	d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "../../private-untrusted-name.exe"}}
	m.Message = ""
	return m
}

func TestAttachmentNormalizationPreservesUntrustedFilenameAndCaptionlessDocuments(t *testing.T) {
	for _, mime := range []string{"application/pdf", "text/plain"} {
		message := attachmentMessage(mime)
		c := imageCandidate(t, message)
		if c.Document == nil || c.Image != nil || c.Document.MIMEType != mime || c.Message.Text != "" || c.Message.Date == "" || c.Document.Filename != "../../private-untrusted-name.exe" {
			t.Fatal("missing captionless document")
		}
		encoded, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"private-document-reference", "AccessHash", "FileReference"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("private media metadata escaped adapter")
			}
		}
	}
}

func TestUnsafeDocumentsExposeNeitherCaptionNorDescriptor(t *testing.T) {
	for name, mutate := range map[string]func(*tg.Message, *tg.MessageMediaDocument, *tg.Document){
		"protected": func(m *tg.Message, _ *tg.MessageMediaDocument, _ *tg.Document) { m.Noforwards = true },
		"forwarded": func(m *tg.Message, _ *tg.MessageMediaDocument, _ *tg.Document) { m.Flags.Set(2) },
		"quoted": func(m *tg.Message, _ *tg.MessageMediaDocument, _ *tg.Document) {
			m.Entities = []tg.MessageEntityClass{&tg.MessageEntityBlockquote{}}
		},
		"ephemeral": func(m *tg.Message, _ *tg.MessageMediaDocument, _ *tg.Document) { m.TTLPeriod = 1 },
		"ttl":       func(_ *tg.Message, m *tg.MessageMediaDocument, _ *tg.Document) { m.TTLSeconds = 1 },
		"spoiler":   func(_ *tg.Message, m *tg.MessageMediaDocument, _ *tg.Document) { m.Spoiler = true },
		"voice":     func(_ *tg.Message, m *tg.MessageMediaDocument, _ *tg.Document) { m.Voice = true },
		"binary": func(_ *tg.Message, _ *tg.MessageMediaDocument, d *tg.Document) {
			d.MimeType = "application/octet-stream"
		},
		"html": func(_ *tg.Message, _ *tg.MessageMediaDocument, d *tg.Document) { d.MimeType = "text/html" },
		"image dimensions": func(_ *tg.Message, _ *tg.MessageMediaDocument, d *tg.Document) {
			d.Attributes = append(d.Attributes, &tg.DocumentAttributeImageSize{W: 2, H: 2})
		},
		"invalid size": func(_ *tg.Message, _ *tg.MessageMediaDocument, d *tg.Document) {
			d.Size = 0
		},
		"oversized filename": func(_ *tg.Message, _ *tg.MessageMediaDocument, d *tg.Document) {
			d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: strings.Repeat("a", 257)}}
		},
		"control filename": func(_ *tg.Message, _ *tg.MessageMediaDocument, d *tg.Document) {
			d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "bad\nname"}}
		},
		"duplicate filename": func(_ *tg.Message, _ *tg.MessageMediaDocument, d *tg.Document) {
			d.Attributes = append(d.Attributes, d.Attributes[0])
		},
		"unsupported attribute": func(_ *tg.Message, _ *tg.MessageMediaDocument, d *tg.Document) {
			d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := attachmentMessage("application/pdf")
			m.Message = "private caption"
			media := m.Media.(*tg.MessageMediaDocument)
			mutate(m, media, media.Document.(*tg.Document))
			c, err := normalizeMessage(testSelfPeer(t), 1, m, map[int64]bool{1: true}, false)
			if err != nil || c.Document != nil || c.Image != nil || c.Message.Text != "" {
				t.Fatal("unsafe document escaped", err)
			}
		})
	}
}

func TestDocumentDownloadRenewsReferenceAndPreservesFiniteBudget(t *testing.T) {
	for _, mime := range []string{"application/pdf", "text/plain"} {
		t.Run(mime, func(t *testing.T) {
			message := attachmentMessage(mime)
			d := message.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
			size := 3 << 20
			if mime == "text/plain" {
				size = model.MaximumTextAttachmentBytes
			}
			d.Size = int64(size)
			expected := imageCandidate(t, message)
			calls, sources := 0, 0
			account := imageTestAccount(t, func() *tg.Message {
				sources++
				if sources == 2 {
					d.FileReference = []byte("renewed")
				}
				return message
			}, func(q *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
				calls++
				if calls == 1 {
					return nil, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
				}
				location, ok := q.Location.(*tg.InputDocumentFileLocation)
				if !ok || location.ThumbSize != "" || !bytes.Equal(location.FileReference, []byte("renewed")) || q.Limit != mediaChunkBytes || q.Offset != int64(calls-2)*mediaChunkBytes || q.CDNSupported {
					t.Fatal("invalid document location, bounds or renewal")
				}
				var kind tg.StorageFileTypeClass = &tg.StorageFileUnknown{}
				if mime == "application/pdf" {
					kind = &tg.StorageFilePdf{}
				}
				return &tg.UploadFile{Type: kind, Bytes: bytes.Repeat([]byte("a"), mediaChunkBytes)}, nil
			})
			data, err := account.DownloadDocument(context.Background(), expected)
			if err != nil || len(data) != size || calls != size/mediaChunkBytes+1 || sources != 2 {
				t.Fatal("bounded document download failed", err)
			}
			if _, err := account.DownloadImage(context.Background(), expected); err == nil {
				t.Fatal("document entered image download")
			}
		})
	}
}

func TestDocumentDownloadRejectsChangedSourceAndTransportMismatch(t *testing.T) {
	for _, change := range []string{"identity", "encoding", "short chunk", "long chunk", "cdn"} {
		t.Run(change, func(t *testing.T) {
			m := attachmentMessage("application/pdf")
			expected := imageCandidate(t, m)
			if change == "identity" {
				m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).ID++
			}
			calls := 0
			account := imageTestAccount(t, func() *tg.Message { return m }, func(*tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
				calls++
				result := &tg.UploadFile{Type: &tg.StorageFilePdf{}, Bytes: make([]byte, 100)}
				switch change {
				case "encoding":
					result.Type = &tg.StorageFileJpeg{}
				case "short chunk":
					result.Bytes = make([]byte, 99)
				case "long chunk":
					result.Bytes = make([]byte, 101)
				case "cdn":
					return &tg.UploadFileCDNRedirect{}, nil
				}
				return result, nil
			})
			if data, err := account.DownloadDocument(context.Background(), expected); err == nil || data != nil {
				t.Fatal("invalid download delivered content")
			}
			if change == "identity" && calls != 0 {
				t.Fatal("changed source downloaded")
			}
		})
	}
}
