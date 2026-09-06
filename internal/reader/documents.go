package reader

import (
	"bytes"
	"unicode"
	"unicode/utf8"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type DocumentContent struct {
	Data     []byte
	MIMEType string
	URI      string
}

type documentItem struct {
	ReplyTo     *model.MessageID          `json:"reply_to,omitempty"`
	ChannelPost *model.ChannelPost        `json:"channel_post,omitempty"`
	Forward     *model.Forward            `json:"forward,omitempty"`
	ID          model.MessageID           `json:"id"`
	Author      model.PeerID              `json:"author"`
	Date        string                    `json:"date"`
	Document    *model.DocumentDescriptor `json:"document"`
}

func (s *Service) documentDescriptor(candidate model.Candidate, grant policy.Grant, authority mediaAuthority) (*model.DocumentDescriptor, error) {
	if candidate.Document == nil {
		return nil, nil
	}
	if !grant.Documents {
		return nil, model.TextError(model.ErrorPolicyDenied, nil)
	}
	source := *candidate.Document
	if candidate.Image != nil || candidate.Voice != nil || !source.IsDocument() {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	operation, _, _ := mediaOperation(mediaDocument)
	handle := mediaHandle{Operation: operation, Message: candidate.Message.ID, Digest: s.mediaDigest(source, mediaDocument), Authority: authority, Expires: grant.Deadline(s.now().Add(mediaLifetime)).Unix()}
	token, err := s.signMediaHandle(handle, mediaDocument)
	if err != nil {
		return nil, err
	}
	return &model.DocumentDescriptor{Handle: token, MIMEType: source.MIMEType, Size: source.Size}, nil
}

// Validate framing and encoding only. PDFs are original untrusted resources;
// this does not parse, sanitize, decrypt, render, extract text, or perform OCR.
func validateDocumentData(source model.MediaSource, data []byte) error {
	if !source.IsDocument() {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := source.Validate(); err != nil {
		return err
	}
	if int64(len(data)) != source.Size {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	switch source.MIMEType {
	case "text/plain":
		if !utf8.Valid(data) {
			return model.TextError(model.ErrorInvalidReference, nil)
		}
		for _, r := range string(data) {
			if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
				return model.TextError(model.ErrorInvalidReference, nil)
			}
		}
	case "application/pdf":
		if len(data) < 14 || !bytes.HasPrefix(data, []byte("%PDF-")) ||
			(string(data[5:8]) != "2.0" && !(data[5] == '1' && data[6] == '.' && data[7] >= '0' && data[7] <= '7')) ||
			(data[8] != '\r' && data[8] != '\n') || !bytes.HasSuffix(bytes.TrimRight(data, " \t\r\n"), []byte("%%EOF")) {
			return model.TextError(model.ErrorInvalidReference, nil)
		}
	}
	return nil
}
