package reader

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type voiceBackend interface {
	DownloadVoice(context.Context, model.Candidate) ([]byte, error)
}
type voiceItem struct {
	AlbumID     string                 `json:"album_id,omitempty"`
	ReplyTo     *model.MessageID       `json:"reply_to,omitempty"`
	ChannelPost *model.ChannelPost     `json:"channel_post,omitempty"`
	Forward     *model.Forward         `json:"forward,omitempty"`
	ID          model.MessageID        `json:"id"`
	Author      model.PeerID           `json:"author"`
	Date        string                 `json:"date"`
	Voice       *model.VoiceDescriptor `json:"voice_note"`
}

func (s *Service) OpenVoice(ctx context.Context, requestID, token string) (Result, error) {
	return s.openMedia(ctx, requestID, token, mediaVoice)
}
func (s *Service) voiceDescriptor(candidate model.Candidate, grant policy.Grant, authority mediaAuthority) (*model.VoiceDescriptor, error) {
	if candidate.Voice == nil {
		return nil, nil
	}
	source := mediaSource(candidate, mediaVoice)
	if !grant.VoiceNotes {
		return nil, model.TextError(model.ErrorPolicyDenied, nil)
	}
	if source == nil || !source.IsVoice() {
		return nil, model.TextError(model.ErrorInvalidReference, nil)
	}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	h := mediaHandle{Operation: "open_voice_note", Message: candidate.Message.ID, Digest: s.mediaDigest(*source, mediaVoice), Authority: authority, Expires: grant.Deadline(s.now().Add(mediaLifetime)).Unix()}
	token, err := s.signMediaHandle(h, mediaVoice)
	if err != nil {
		return nil, err
	}
	return &model.VoiceDescriptor{Handle: token, MIMEType: source.MIMEType, Size: source.Size, Duration: source.Duration}, nil
}

// Validate bounded Ogg framing and Opus headers, not decoded audio or transcription.
// Only one mono/stereo stream with mapping family zero is supported.
// Some encoders omit or set EOS early. The declared file boundary and validated
// packet continuity determine completion; EOS does not permit a new stream.
func validateVoiceData(source model.MediaSource, data []byte) error {
	invalid := func() error { return model.TextError(model.ErrorInvalidReference, nil) }
	if !source.IsVoice() {
		return invalid()
	}
	if err := source.Validate(); err != nil {
		return err
	}
	if int64(len(data)) != source.Size {
		return invalid()
	}
	var serial, sequence uint32
	var granule uint64
	var preSkip uint16
	packets := 0
	packet := []byte{}
	defer func() { clear(packet[:cap(packet)]) }()
	for offset := 0; offset < len(data); {
		if len(data)-offset < 27 {
			return invalid()
		}
		page := data[offset:]
		if !bytes.Equal(page[:4], []byte("OggS")) || page[4] != 0 || page[5]&^byte(7) != 0 {
			return invalid()
		}
		flags := page[5]
		segments := int(page[26])
		if segments == 0 || len(page) < 27+segments {
			return invalid()
		}
		size := 27 + segments
		for _, n := range page[27 : 27+segments] {
			size += int(n)
		}
		if size > len(page) {
			return invalid()
		}
		page = page[:size]
		if oggChecksum(page) != binary.LittleEndian.Uint32(page[22:26]) {
			return invalid()
		}
		currentSerial := binary.LittleEndian.Uint32(page[14:18])
		currentSequence := binary.LittleEndian.Uint32(page[18:22])
		if offset == 0 {
			serial = currentSerial
			if flags != 2 || currentSequence != 0 {
				return invalid()
			}
		} else if currentSerial != serial || currentSequence != sequence || flags&2 != 0 {
			return invalid()
		}
		sequence = currentSequence + 1
		if (flags&1 != 0) != (len(packet) > 0) {
			return invalid()
		}
		currentGranule := binary.LittleEndian.Uint64(page[6:14])
		if currentGranule != math.MaxUint64 {
			if currentGranule < granule {
				return invalid()
			}
			granule = currentGranule
		}
		body := 27 + segments
		for i, n := range page[27 : 27+segments] {
			packet = append(packet, page[body:body+int(n)]...)
			body += int(n)
			if len(packet) > 65536 {
				return invalid()
			}
			if n == 255 {
				continue
			}
			switch packets {
			case 0:
				if offset != 0 || i != segments-1 || len(packet) != 19 || !bytes.Equal(packet[:8], []byte("OpusHead")) || packet[8] != 1 || (packet[9] != 1 && packet[9] != 2) || packet[18] != 0 || currentGranule != 0 {
					return invalid()
				}
				preSkip = binary.LittleEndian.Uint16(packet[10:12])
			case 1:
				if !validOpusTags(packet) || i != segments-1 || currentGranule != 0 {
					return invalid()
				}
			default:
				if len(packet) == 0 {
					return invalid()
				}
			}
			packets++
			packet = packet[:0]
		}
		if (flags&4 != 0 || offset+size == len(data)) && (len(packet) != 0 || currentGranule == math.MaxUint64) {
			return invalid()
		}
		offset += size
	}
	if packets < 3 || granule <= uint64(preSkip) {
		return invalid()
	}
	samples := granule - uint64(preSkip)
	if samples > uint64(model.MaximumVoiceDuration)*48000 {
		return model.TextError(model.ErrorMediaTooLarge, nil)
	}
	seconds := int((samples + 47999) / 48000)
	if seconds < source.Duration-1 || seconds > source.Duration+1 {
		return invalid()
	}
	return nil
}

func validOpusTags(packet []byte) bool {
	if len(packet) < 16 || !bytes.Equal(packet[:8], []byte("OpusTags")) {
		return false
	}
	remaining := packet[8:]
	consume := func() bool {
		if len(remaining) < 4 {
			return false
		}
		n := uint64(binary.LittleEndian.Uint32(remaining))
		remaining = remaining[4:]
		if n > uint64(len(remaining)) {
			return false
		}
		remaining = remaining[int(n):]
		return true
	}
	if !consume() || len(remaining) < 4 {
		return false
	}
	count := binary.LittleEndian.Uint32(remaining)
	remaining = remaining[4:]
	if uint64(count) > uint64(len(remaining)/4) {
		return false
	}
	for i := uint32(0); i < count; i++ {
		if !consume() {
			return false
		}
	}
	return true // RFC 7845 permits padding after the comment vector.
}

func oggChecksum(page []byte) uint32 {
	var crc uint32
	for i, b := range page {
		if i >= 22 && i < 26 {
			b = 0
		}
		crc ^= uint32(b) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
