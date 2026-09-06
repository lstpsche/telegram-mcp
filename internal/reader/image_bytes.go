package reader

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func validateImageData(source model.MediaSource, data []byte) error {
	if source.IsDocument() {
		return model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := source.Validate(); err != nil {
		return err
	}
	if len(data) > model.MaximumImageBytes {
		return model.TextError(model.ErrorMediaTooLarge, nil)
	}
	if int64(len(data)) != source.Size {
		return invalidImage("image byte count does not match metadata")
	}
	var format string
	switch source.MIMEType {
	case "image/jpeg":
		format = "jpeg"
		if !completeJPEG(data) {
			return invalidImage("invalid JPEG framing")
		}
	case "image/png":
		format = "png"
		if !completeStaticPNG(data) {
			return invalidImage("invalid static PNG framing")
		}
	}
	config, decodedFormat, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return model.TextError(model.ErrorInvalidReference, err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > model.MaximumImageDimension || config.Height > model.MaximumImageDimension || int64(config.Width)*int64(config.Height) > model.MaximumImagePixels {
		return model.TextError(model.ErrorMediaTooLarge, nil)
	}
	if decodedFormat != format || config.Width != source.Width || config.Height != source.Height {
		return invalidImage("image dimensions or encoding do not match metadata")
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return model.TextError(model.ErrorInvalidReference, err)
	}
	if decodedFormat != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return invalidImage("decoded image does not match metadata")
	}
	return nil
}

func invalidImage(reason string) error {
	return model.TextError(model.ErrorInvalidReference, errors.New(reason))
}

// Decoders may stop at the first image and buffer trailing bytes. Validate the
// container separately so appended content cannot pass through a native image.
func completeStaticPNG(data []byte) bool {
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return false
	}
	for offset := 8; len(data)-offset >= 12; {
		length := uint64(binary.BigEndian.Uint32(data[offset:]))
		if length > uint64(len(data)-offset-12) {
			return false
		}
		kind := string(data[offset+4 : offset+8])
		offset += int(length) + 12
		switch kind {
		case "acTL", "fcTL", "fdAT":
			return false
		case "IEND":
			return length == 0 && offset == len(data)
		}
	}
	return false
}

func completeJPEG(data []byte) bool {
	if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		return false
	}
	for offset := 2; offset < len(data); {
		if data[offset] != 0xff {
			return false
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset == len(data) {
			return false
		}
		marker := data[offset]
		offset++
		if marker == 0xd9 {
			return offset == len(data)
		}
		if marker == 0 || marker == 0xd8 || (marker >= 0xd0 && marker <= 0xd7) {
			return false
		}
		if marker == 1 { // TEM has no length field.
			continue
		}
		if len(data)-offset < 2 {
			return false
		}
		length := int(binary.BigEndian.Uint16(data[offset:]))
		if length < 2 || length > len(data)-offset {
			return false
		}
		offset += length
		if marker != 0xda {
			continue
		}
		// Entropy-coded scans escape literal FF bytes and may contain restart
		// markers. Other markers resume normal parsing, including later scans.
		for offset < len(data) {
			if data[offset] != 0xff {
				offset++
				continue
			}
			start := offset
			for offset < len(data) && data[offset] == 0xff {
				offset++
			}
			if offset == len(data) {
				return false
			}
			if data[offset] == 0 || (data[offset] >= 0xd0 && data[offset] <= 0xd7) {
				offset++
				continue
			}
			offset = start
			break
		}
	}
	return false
}
