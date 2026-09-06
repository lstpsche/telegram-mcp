package reader

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func voiceTestPage(sequence uint32, flags byte, granule uint64, packet []byte) []byte {
	page := make([]byte, 28+len(packet))
	copy(page, "OggS")
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], 123)
	binary.LittleEndian.PutUint32(page[18:22], sequence)
	page[26], page[27] = 1, byte(len(packet))
	copy(page[28:], packet)
	binary.LittleEndian.PutUint32(page[22:26], oggChecksum(page))
	return page
}

func voiceTestData() []byte {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8], head[9] = 1, 1
	binary.LittleEndian.PutUint32(head[12:16], 48000)
	tags := make([]byte, 16)
	copy(tags, "OpusTags")
	data := voiceTestPage(0, 2, 0, head)
	data = append(data, voiceTestPage(1, 0, 0, tags)...)
	// A 20 ms Opus silence packet.
	return append(data, voiceTestPage(2, 4, 960, []byte{0xf8, 0xff, 0xfe})...)
}

func voiceTestSource(data []byte) model.MediaSource {
	return model.MediaSource{Kind: "voice", MIMEType: "audio/ogg", Size: int64(len(data)), Duration: 1, Fingerprint: strings.Repeat("a", 64)}
}

func TestVoiceDataRequiresCompleteOriginalOggOpus(t *testing.T) {
	data := voiceTestData()
	if err := validateVoiceData(voiceTestSource(data), data); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated":     func(b []byte) []byte { return b[:len(b)-1] },
		"trailing":      func(b []byte) []byte { return append(b, 0) },
		"checksum":      func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		"chained":       func(b []byte) []byte { return append(b, voiceTestData()...) },
		"missing_audio": func(b []byte) []byte { return b[:91] },
	} {
		t.Run(name, func(t *testing.T) {
			broken := mutate(append([]byte(nil), data...))
			if err := validateVoiceData(voiceTestSource(broken), broken); err == nil {
				t.Fatal("malformed stream accepted")
			}
		})
	}
}

func TestVoiceDataRejectsInvalidStreamWithValidChecksum(t *testing.T) {
	for name, mutate := range map[string]func([]byte){
		"sequence":          func(p []byte) { binary.LittleEndian.PutUint32(p[18:22], 9) },
		"serial":            func(p []byte) { binary.LittleEndian.PutUint32(p[14:18], 456) },
		"continuation":      func(p []byte) { p[5] |= 1 },
		"no_end":            func(p []byte) { p[5] = 0 },
		"too_long":          func(p []byte) { binary.LittleEndian.PutUint64(p[6:14], 301*48000) },
		"duration_mismatch": func(p []byte) { binary.LittleEndian.PutUint64(p[6:14], 10*48000) },
	} {
		t.Run(name, func(t *testing.T) {
			data := voiceTestData()
			page := data[91:]
			mutate(page)
			binary.LittleEndian.PutUint32(page[22:26], oggChecksum(page))
			if err := validateVoiceData(voiceTestSource(data), data); err == nil {
				t.Fatal("invalid stream accepted")
			}
		})
	}
}

func (f *imageFake) DownloadVoice(ctx context.Context, c model.Candidate) ([]byte, error) {
	return f.DownloadImage(ctx, c)
}

func voiceService(t *testing.T) (*Service, *imageFake, *policy.Repository, policy.Grant, string) {
	t.Helper()
	s, f, p, db, g := testService(t)
	g.VoiceNotes = true
	saveGrant(t, p, g)
	data := voiceTestData()
	c := candidate(g, 20, "")
	source := voiceTestSource(data)
	c.Voice = &source
	f.items = []model.Candidate{c}
	backend := &imageFake{fakeBackend: f, db: db, data: data}
	s.backend = backend
	result, err := s.Search(context.Background(), "req_voice_discovery", g.Peer, "caption", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[model.SearchHit]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 || envelope.Items[0].Voice == nil || f.ackCalls != 0 || backend.downloads != 0 {
		t.Fatal("voice discovery touched content or omitted descriptor")
	}
	f.historyCalls = 0
	return s, backend, p, g, envelope.Items[0].Voice.Handle
}

func TestVoiceDeliveryPreservesBytesAndReportsHistoryReceipt(t *testing.T) {
	s, f, _, _, token := voiceService(t)
	want := bytes.Clone(f.data)
	result, err := s.OpenVoice(context.Background(), "req_voice_open", token)
	if err != nil {
		t.Fatal(err)
	}
	if result.Voice == nil || result.Image != nil || result.Document != nil || !bytes.Equal(result.Voice.Data, want) || result.Voice.MIMEType != "audio/ogg" || f.ackCalls != 1 || f.downloads != 1 || f.historyCalls != 3 {
		t.Fatal("incorrect voice delivery")
	}
	var envelope model.Envelope[voiceItem]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 || envelope.Items[0].Voice.Handle != token || envelope.ReadEffect.Kind != model.ReadEffectHistoryMarkedRead || !envelope.UntrustedContent {
		t.Fatal("missing provenance")
	}
}

func TestVoicePermissionAndHandleDomains(t *testing.T) {
	s, f, p, g, token := voiceService(t)
	if _, err := s.OpenImage(context.Background(), "req_voice_image", token); err == nil {
		t.Fatal("voice handle accepted as image")
	}
	if _, err := s.OpenDocument(context.Background(), "req_voice_document", token); err == nil {
		t.Fatal("voice handle accepted as document")
	}
	g.VoiceNotes = false
	g.Images, g.Documents = true, true
	saveGrant(t, p, g)
	result, err := s.OpenVoice(context.Background(), "req_voice_denied", token)
	if err == nil || result.Voice != nil || f.downloads != 0 || f.ackCalls != 0 || f.historyCalls != 0 {
		t.Fatal("other media permissions granted voice access")
	}
}

func TestVoiceFailuresWithholdAndClearAudio(t *testing.T) {
	for _, failure := range []string{"bytes", "source", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			s, f, _, _, token := voiceService(t)
			switch failure {
			case "bytes":
				f.data[len(f.data)-1] ^= 1
			case "source":
				f.onDownload = func() { f.items[0].Voice.Fingerprint = strings.Repeat("b", 64) }
			case "receipt":
				f.ackError = errors.New("synthetic receipt failure")
			}
			result, err := s.OpenVoice(context.Background(), "req_voice_failure", token)
			if err == nil || result.Voice != nil || len(result.JSON) != 0 || f.downloads != 1 {
				t.Fatal("failure released voice")
			}
			if !bytes.Equal(f.data, make([]byte, len(f.data))) {
				t.Fatal("failure retained audio")
			}
		})
	}
}
