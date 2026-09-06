package reader

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type imageFake struct {
	*fakeBackend
	db            *sql.DB
	data          []byte
	downloads     int
	downloadError error
	onDownload    func()
}

func (f *imageFake) DownloadImage(context.Context, model.Candidate) ([]byte, error) {
	f.downloads++
	if f.onDownload != nil {
		f.onDownload()
	}
	return f.data, f.downloadError
}
func imageService(t *testing.T) (*Service, *imageFake, *policy.Repository, policy.Grant, string) {
	t.Helper()
	s, f, p, db, g := testService(t)
	g.Images = true
	saveGrant(t, p, g)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	c := candidate(g, 20, "")
	c.Image = &model.MediaSource{Kind: "document", MIMEType: "image/png", Width: 2, Height: 3, Size: int64(data.Len()), Fingerprint: strings.Repeat("a", 64)}
	f.items = []model.Candidate{c}
	backend := &imageFake{fakeBackend: f, db: db, data: data.Bytes()}
	s.backend = backend
	result, err := s.Search(context.Background(), "req_image_discovery", g.Peer, "caption", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[model.SearchHit]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 || envelope.Items[0].Image == nil || envelope.ReadEffect.Kind != model.ReadEffectNone || f.ackCalls != 0 || backend.downloads != 0 {
		t.Fatal("discovery fetched bytes or omitted metadata")
	}
	f.historyCalls = 0
	return s, backend, p, g, envelope.Items[0].Image.Handle
}

func TestOpenImageValidatesAndAcknowledgesBeforeNativeDelivery(t *testing.T) {
	s, f, _, _, handle := imageService(t)
	result, err := s.OpenImage(context.Background(), "req_image_open", handle)
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[imageItem]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if result.Image == nil || !bytes.Equal(result.Image.Data, f.data) || result.Image.MIMEType != "image/png" || f.downloads != 1 || f.ackCalls != 1 || f.historyCalls != 3 || envelope.ReadEffect.Kind != model.ReadEffectHistoryMarkedRead || len(envelope.Items) != 1 || envelope.Items[0].Image.Handle != handle {
		t.Fatal("incomplete native image delivery")
	}
	if strings.Contains(string(result.JSON), base64.StdEncoding.EncodeToString(f.data)) || strings.Contains(string(result.JSON), strings.Repeat("a", 64)) {
		t.Fatal("bytes or fingerprint in metadata")
	}
	// The signing key and policy binding survive service reconstruction.
	restarted, err := New(f, s.policy, s.now, s.cursorKey[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.OpenImage(context.Background(), "req_image_restart", handle); err != nil {
		t.Fatal(err)
	}
}

func TestImageDiscoveryNeedsExplicitGrant(t *testing.T) {
	s, f, p, g, _ := imageService(t)
	g.Images = false
	saveGrant(t, p, g)
	result, err := s.Messages(context.Background(), "req_image_denied", request(g))
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[model.Message]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 0 || !envelope.Partial || f.ackCalls != 0 || f.downloads != 0 {
		t.Fatal("text grant exposed image")
	}
	g.Images = true
	saveGrant(t, p, g)
	result, err = s.Messages(context.Background(), "req_image_history", request(g))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 || envelope.Items[0].Text != "" || envelope.Items[0].Image == nil || f.ackCalls != 1 || f.downloads != 0 {
		t.Fatal("captionless image history failed")
	}
}

func TestImageHandleDenialsPrecedeDownload(t *testing.T) {
	for _, name := range []string{"tamper", "wrong_domain", "noncanonical", "expired", "key", "revision", "disabled", "read_prefix", "epoch"} {
		t.Run(name, func(t *testing.T) {
			s, f, p, g, handle := imageService(t)
			switch name {
			case "tamper":
				handle += "x"
			case "wrong_domain":
				handle = "sc1" + handle[3:]
			case "noncanonical":
				parts := strings.Split(handle, ".")
				raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
				parts[1] = base64.RawURLEncoding.EncodeToString(append([]byte(" "), raw...))
				parts[2] = base64.RawURLEncoding.EncodeToString(s.mediaMAC("image-handle-v1", parts[1]))
				handle = strings.Join(parts, ".")
			case "expired":
				now := s.now().Add(mediaLifetime)
				s.now = func() time.Time { return now }
			case "key":
				s.cursorKey[0]++
			case "revision":
				saveGrant(t, p, g)
			case "disabled":
				g.Images = false
				saveGrant(t, p, g)
			case "read_prefix":
				g.ReadThrough = 19
				saveGrant(t, p, g)
				l, err := p.Acquire(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				epoch, rev, err := l.Binding(context.Background())
				l.Close()
				if err != nil {
					t.Fatal(err)
				}
				d, err := s.imageDescriptor(f.items[0], g, mediaAuthority{epoch, rev})
				if err != nil {
					t.Fatal(err)
				}
				handle = d.Handle
			case "epoch":
				h, err := s.decodeMediaHandle(handle, false)
				if err != nil {
					t.Fatal(err)
				}
				h.Authority.Epoch = strings.Repeat("z", 43)
				raw, _ := json.Marshal(h)
				payload := base64.RawURLEncoding.EncodeToString(raw)
				handle = "im1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.mediaMAC("image-handle-v1", payload))
			}
			result, err := s.OpenImage(context.Background(), "req_image_invalid", handle)
			if err == nil || result.Image != nil || len(result.JSON) != 0 || f.downloads != 0 || f.historyCalls != 0 || f.ackCalls != 0 {
				t.Fatalf("denial too late: %v", err)
			}
		})
	}
}

func TestImageFailuresNeverReleaseBytes(t *testing.T) {
	for _, name := range []string{"download", "corrupt", "replacement", "deleted", "after_download", "ack", "after_ack", "readiness", "expiry", "audit", "cancel"} {
		t.Run(name, func(t *testing.T) {
			s, f, _, _, handle := imageService(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			uncertain := false
			switch name {
			case "download":
				f.downloadError = errors.New("synthetic download failure")
			case "corrupt":
				f.data = bytes.Repeat([]byte{0}, len(f.data))
			case "replacement":
				f.items[0].Image.Fingerprint = strings.Repeat("b", 64)
			case "deleted":
				f.items = nil
			case "after_download":
				f.onDownload = func() { f.items[0].Image.Fingerprint = strings.Repeat("b", 64) }
			case "ack":
				f.ackError = errors.New("synthetic receipt failure")
				uncertain = true
			case "after_ack":
				f.onAck = func() { f.items = nil }
				uncertain = true
			case "readiness":
				f.onDownload = func() { f.notReady = true }
			case "expiry":
				f.onDownload = func() { now := s.now().Add(mediaLifetime); s.now = func() time.Time { return now } }
			case "audit":
				if _, err := f.db.Exec("DROP TABLE text_audit"); err != nil {
					t.Fatal(err)
				}
				uncertain = true
			case "cancel":
				f.onDownload = cancel
			}
			result, err := s.OpenImage(ctx, "req_image_failure", handle)
			if err == nil || result.Image != nil || len(result.JSON) != 0 {
				t.Fatalf("failed open released result: %v", err)
			}
			if uncertain && model.TextErrorCategory(err) != model.ErrorReadEffectUncertain {
				t.Fatalf("lost possible effect: %v", err)
			}
			if !uncertain && f.ackCalls != 0 {
				t.Fatal("acknowledged before validation")
			}
		})
	}
}

func FuzzImageHandle(f *testing.F) {
	f.Add("im1.e30.invalid")
	f.Add("")
	s := &Service{now: func() time.Time { return time.Unix(1000, 0) }}
	peer, _ := model.NewPeerID(model.PeerKindChat, 42)
	id, _ := model.NewMessageID(peer, 20)
	handle := mediaHandle{Operation: "open_image", Message: id, Digest: strings.Repeat("a", 43), Authority: mediaAuthority{Epoch: strings.Repeat("e", 43), Revision: 1}, Expires: 1100}
	raw, _ := json.Marshal(handle)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	f.Add("im1." + payload + "." + base64.RawURLEncoding.EncodeToString(s.mediaMAC("image-handle-v1", payload)))
	f.Fuzz(func(t *testing.T, token string) {
		h, err := s.decodeMediaHandle(token, false)
		if err == nil && (h.Operation != "open_image" || h.Expires <= s.now().Unix() || h.Expires > s.now().Add(mediaLifetime).Unix() || h.Message.String() == "") {
			t.Fatal("invalid accepted handle")
		}
	})
}
