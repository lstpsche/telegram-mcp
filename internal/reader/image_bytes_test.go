package reader

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func syntheticImageBytes(t testing.TB, mime string) (model.MediaSource, []byte) {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, 3, 2))
	pixels.Set(1, 1, color.RGBA{R: 200, G: 120, B: 30, A: 255})
	var encoded bytes.Buffer
	var err error
	if mime == "image/png" {
		err = png.Encode(&encoded, pixels)
	} else {
		err = jpeg.Encode(&encoded, pixels, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return model.MediaSource{Kind: "document", MIMEType: mime, Width: 3, Height: 2, Size: int64(encoded.Len()), Fingerprint: strings.Repeat("a", 64)}, encoded.Bytes()
}

func TestValidateImageData(t *testing.T) {
	for _, mime := range []string{"image/jpeg", "image/png"} {
		t.Run(mime, func(t *testing.T) {
			source, data := syntheticImageBytes(t, mime)
			original := bytes.Clone(data)
			if err := validateImageData(source, data); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, original) {
				t.Fatal("validation mutated the original image")
			}
			if mime == "image/jpeg" {
				source.Kind = "photo"
				if err := validateImageData(source, data); err != nil {
					t.Fatal(err)
				}
			}
			cases := []struct {
				name     string
				change   func(*model.MediaSource, []byte) []byte
				category model.ErrorCategory
			}{
				{"declared-size", func(s *model.MediaSource, b []byte) []byte { s.Size++; return b }, model.ErrorInvalidReference},
				{"dimensions", func(s *model.MediaSource, b []byte) []byte { s.Width++; return b }, model.ErrorInvalidReference},
				{"mime", func(s *model.MediaSource, b []byte) []byte {
					s.Kind = "document"
					if s.MIMEType == "image/jpeg" {
						s.MIMEType = "image/png"
					} else {
						s.MIMEType = "image/jpeg"
					}
					return b
				}, model.ErrorInvalidReference},
				{"fingerprint", func(s *model.MediaSource, b []byte) []byte { s.Fingerprint = "private"; return b }, model.ErrorInvalidReference},
				{"oversized-source", func(s *model.MediaSource, b []byte) []byte { s.Size = model.MaximumImageBytes + 1; return b }, model.ErrorMediaTooLarge},
				{"oversized-body", func(s *model.MediaSource, b []byte) []byte { return make([]byte, model.MaximumImageBytes+1) }, model.ErrorMediaTooLarge},
				{"truncated", func(s *model.MediaSource, b []byte) []byte { b = b[:len(b)-1]; s.Size = int64(len(b)); return b }, model.ErrorInvalidReference},
				{"trailing-data", func(s *model.MediaSource, b []byte) []byte {
					b = append(b, []byte("private trailing payload")...)
					s.Size = int64(len(b))
					return b
				}, model.ErrorInvalidReference},
				{"concatenated-images", func(s *model.MediaSource, b []byte) []byte { b = append(b, b...); s.Size = int64(len(b)); return b }, model.ErrorInvalidReference},
				{"bad-magic", func(s *model.MediaSource, b []byte) []byte { b[0] = 0; return b }, model.ErrorInvalidReference},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					s := source
					b := tc.change(&s, bytes.Clone(data))
					err := validateImageData(s, b)
					if model.TextErrorCategory(err) != tc.category {
						t.Fatalf("category = %q, want %q", model.TextErrorCategory(err), tc.category)
					}
					if strings.Contains(err.Error(), "private") {
						t.Fatal("error disclosed supplied data")
					}
				})
			}
		})
	}
}

func pngChunk(kind string, payload []byte) []byte {
	chunk := make([]byte, len(payload)+12)
	binary.BigEndian.PutUint32(chunk, uint32(len(payload)))
	copy(chunk[4:8], kind)
	copy(chunk[8:], payload)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	return chunk
}

func TestValidateImageDataPNGStructure(t *testing.T) {
	source, original := syntheticImageBytes(t, "image/png")
	for _, kind := range []string{"acTL", "fcTL", "fdAT"} {
		t.Run(kind, func(t *testing.T) {
			data := append(bytes.Clone(original[:33]), pngChunk(kind, make([]byte, 8))...)
			data = append(data, original[33:]...)
			s := source
			s.Size = int64(len(data))
			if model.TextErrorCategory(validateImageData(s, data)) != model.ErrorInvalidReference {
				t.Fatal("accepted animated PNG extension")
			}
		})
	}
	for _, dimensions := range [][2]uint32{{4097, 2}, {2001, 2000}} {
		data := bytes.Clone(original)
		binary.BigEndian.PutUint32(data[16:20], dimensions[0])
		binary.BigEndian.PutUint32(data[20:24], dimensions[1])
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		if model.TextErrorCategory(validateImageData(source, data)) != model.ErrorMediaTooLarge {
			t.Fatal("excessive dimensions did not fail before full decoding")
		}
	}
	for _, mutate := range []func([]byte){
		func(data []byte) { data[29] ^= 1 }, // IHDR checksum.
		func(data []byte) { binary.BigEndian.PutUint32(data[8:12], ^uint32(0)) },
		func(data []byte) { data[len(data)-1] ^= 1 }, // IEND checksum.
		func(data []byte) { data[45] ^= 1 },          // Compressed stream.
	} {
		data := bytes.Clone(original)
		mutate(data)
		if model.TextErrorCategory(validateImageData(source, data)) != model.ErrorInvalidReference {
			t.Fatal("accepted corrupt PNG")
		}
	}
}

func TestValidateImageDataJPEGStructure(t *testing.T) {
	source, original := syntheticImageBytes(t, "image/jpeg")
	frame := bytes.Index(original, []byte{0xff, 0xc0})
	if frame < 0 {
		t.Fatal("synthetic JPEG has no baseline frame")
	}
	for _, dimensions := range [][2]uint16{{4097, 2}, {2001, 2000}} {
		data := bytes.Clone(original)
		binary.BigEndian.PutUint16(data[frame+5:frame+7], dimensions[1])
		binary.BigEndian.PutUint16(data[frame+7:frame+9], dimensions[0])
		if model.TextErrorCategory(validateImageData(source, data)) != model.ErrorMediaTooLarge {
			t.Fatal("excessive JPEG dimensions did not fail before full decoding")
		}
	}
	// EOI-like bytes inside a length-delimited metadata segment are not the end.
	data := append(bytes.Clone(original[:2]), []byte{0xff, 0xe1, 0, 4, 0xff, 0xd9}...)
	data = append(data, original[2:]...)
	source.Size = int64(len(data))
	if err := validateImageData(source, data); err != nil {
		t.Fatal("rejected legal metadata segment", err)
	}
	for _, malformed := range [][]byte{
		{0xff, 0xd8, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xe1, 0, 1, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xda, 0, 2, 0xff},
		append(bytes.Clone(original[:len(original)-5]), 0xff, 0xd9),
	} {
		source.Size = int64(len(malformed))
		if model.TextErrorCategory(validateImageData(source, malformed)) != model.ErrorInvalidReference {
			t.Fatal("accepted malformed or incomplete JPEG")
		}
	}
}

func FuzzValidateImageData(f *testing.F) {
	for _, mime := range []string{"image/jpeg", "image/png"} {
		_, data := syntheticImageBytes(f, mime)
		f.Add(mime, data)
	}
	f.Fuzz(func(t *testing.T, mime string, data []byte) {
		source := model.MediaSource{Kind: "document", MIMEType: mime, Width: 3, Height: 2, Size: int64(len(data)), Fingerprint: strings.Repeat("a", 64)}
		if err := validateImageData(source, data); err == nil {
			decoded, _, err := image.Decode(bytes.NewReader(data))
			if err != nil || decoded.Bounds().Dx() != 3 || decoded.Bounds().Dy() != 2 {
				t.Fatal("accepted bytes that do not decode to the declared image")
			}
		}
	})
}
