package model

import (
	"regexp"
	"unicode"
	"unicode/utf8"
)

const (
	MaximumImageBytes       = 1 << 20
	MaximumImagePixels      = 4_000_000
	MaximumImageDimension   = 4096
	MaximumMediaResultBytes = 2 << 20
)

var imageFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MediaSource is transient adapter evidence, never a public file location.
type MediaSource struct {
	Filename    string `json:",omitempty"`
	Kind        string
	MIMEType    string
	Width       int
	Height      int
	Size        int64
	Duration    int `json:",omitempty"`
	Fingerprint string
}

func (i MediaSource) IsDocument() bool {
	return i.Kind == "document" && (i.MIMEType == "application/pdf" || IsTextDocumentMIME(i.MIMEType))
}

func (i MediaSource) IsVoice() bool { return i.Kind == "voice" && i.MIMEType == "audio/ogg" }
func (i MediaSource) Validate() error {
	if !ValidAttachmentFilename(i.Filename) || i.Kind == "photo" && i.Filename != "" {
		return TextError(ErrorInvalidReference, nil)
	}
	if i.IsVoice() {
		if i.Width != 0 || i.Height != 0 || i.Duration <= 0 || i.Duration > MaximumVoiceDuration || !imageFingerprintPattern.MatchString(i.Fingerprint) {
			return TextError(ErrorInvalidReference, nil)
		}
		if i.Size <= 0 || i.Size > MaximumVoiceBytes {
			return TextError(ErrorMediaTooLarge, nil)
		}
		return nil
	}
	if i.Duration != 0 {
		return TextError(ErrorInvalidReference, nil)
	}
	if i.IsDocument() {
		if i.Width != 0 || i.Height != 0 || !imageFingerprintPattern.MatchString(i.Fingerprint) {
			return TextError(ErrorInvalidReference, nil)
		}
		if i.Size <= 0 || (IsTextDocumentMIME(i.MIMEType) && i.Size > MaximumTextAttachmentBytes) {
			return TextError(ErrorMediaTooLarge, nil)
		}
		return nil
	}
	if (i.Kind != "photo" && i.Kind != "document") || (i.MIMEType != "image/jpeg" && i.MIMEType != "image/png") || (i.Kind == "photo" && i.MIMEType != "image/jpeg") || !imageFingerprintPattern.MatchString(i.Fingerprint) {
		return TextError(ErrorInvalidReference, nil)
	}
	if i.Width <= 0 || i.Height <= 0 || i.Width > MaximumImageDimension || i.Height > MaximumImageDimension || int64(i.Width)*int64(i.Height) > MaximumImagePixels || i.Size <= 0 || i.Size > MaximumImageBytes {
		return TextError(ErrorMediaTooLarge, nil)
	}
	return nil
}

// ImageDescriptor exposes only bounded metadata and a reauthorized opaque handle.
type ImageDescriptor struct {
	Filename string `json:"filename,omitempty"`
	Handle   string `json:"handle"`
	Kind     string `json:"kind"`
	MIMEType string `json:"mime_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Size     int64  `json:"size"`
}

const MaximumTextAttachmentBytes = 256 << 10

// DocumentDescriptor exposes untrusted display metadata, never a download location.
type DocumentDescriptor struct {
	Filename string `json:"filename,omitempty"`
	Handle   string `json:"handle"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

const MaximumVoiceBytes = 1 << 20
const MaximumVoiceDuration = 300

// VoiceDescriptor provides separate capabilities for original audio and Telegram transcription.
type VoiceDescriptor struct {
	Filename            string `json:"filename,omitempty"`
	Handle              string `json:"handle"`
	TranscriptionHandle string `json:"transcription_handle,omitempty"`
	MIMEType            string `json:"mime_type"`
	Size                int64  `json:"size"`
	Duration            int    `json:"duration_seconds"`
}

// ValidAttachmentFilename bounds display metadata without interpreting a path.
func ValidAttachmentFilename(value string) bool {
	if len(value) > 1024 || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// IsTextDocumentMIME selects original UTF-8 delivery, never a parser or execution mode.
func IsTextDocumentMIME(mime string) bool {
	switch mime {
	case "text/plain", "text/markdown", "text/csv", "application/json":
		return true
	default:
		return false
	}
}

// MaximumVoiceTranscriptBytes bounds transient Telegram speech recognition text.
const MaximumVoiceTranscriptBytes = 16 << 10

// VoiceTranscript is untrusted Telegram-generated text, not a verbatim guarantee.
type VoiceTranscript struct {
	Text string `json:"text"`
}

func (t VoiceTranscript) Validate() error {
	if len(t.Text) > MaximumVoiceTranscriptBytes {
		return TextError(ErrorResultTooLarge, nil)
	}
	if !utf8.ValidString(t.Text) {
		return TextError(ErrorTranscriptionFailed, nil)
	}
	return nil
}
