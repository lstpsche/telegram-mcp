package model

import "regexp"

const (
	MaximumImageBytes       = 1 << 20
	MaximumImagePixels      = 4_000_000
	MaximumImageDimension   = 4096
	MaximumImageResultBytes = 2 << 20
)

var imageFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ImageSource is transient adapter evidence, never a public file location.
type ImageSource struct {
	Kind        string
	MIMEType    string
	Width       int
	Height      int
	Size        int64
	Fingerprint string
}

func (i ImageSource) Validate() error {
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
	Handle   string `json:"handle"`
	Kind     string `json:"kind"`
	MIMEType string `json:"mime_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Size     int64  `json:"size"`
}
