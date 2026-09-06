package reader

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func TestForwardedCopiesRequireConsentedContainerAuthority(t *testing.T) {
	s, f, p, _, g := testService(t)
	c := candidate(g, 20, "forwarded text")
	c.Forwarded = true
	c.Message.Forward = &model.Forward{Date: "2026-09-01T00:00:00Z", FromPeer: "tgpeer:v1:channel:99"}
	f.items = []model.Candidate{c}
	for _, profile := range []string{policy.ProfileSelfAuthored, policy.ProfileConsented} {
		g.Profile = profile
		saveGrant(t, p, g)
		result, err := s.Search(context.Background(), "req_forward_search", g.Peer, "text", 20, "")
		if err != nil {
			t.Fatal(err)
		}
		var envelope model.Envelope[model.SearchHit]
		if err := json.Unmarshal(result.JSON, &envelope); err != nil {
			t.Fatal(err)
		}
		want := 0
		if profile == policy.ProfileConsented {
			want = 1
		}
		if len(envelope.Items) != want {
			t.Fatal("incorrect forward authority")
		}
		if want == 1 && (envelope.Items[0].Forward == nil || envelope.Items[0].Author != g.Author) {
			t.Fatal("lost attribution")
		}
	}
	g.Author, _ = model.NewPeerID(model.PeerKindUser, 99)
	saveGrant(t, p, g)
	if err := g.CheckMessage(c, f.SelfID(), s.now()); err == nil {
		t.Fatal("origin authorized container")
	}
	setFullRead(t, p, true)
	if _, err := s.Messages(context.Background(), "req_forward_context", model.HistoryQuery{Peer: g.Peer, Target: 20, Limit: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestForwardChangeDuringMediaDeliveryWithholdsBytes(t *testing.T) {
	for _, duringAck := range []bool{false, true} {
		t.Run(map[bool]string{false: "download", true: "acknowledgment"}[duringAck], func(t *testing.T) {
			testForwardChangeWithholdsBytes(t, duringAck)
		})
	}
}

func testForwardChangeWithholdsBytes(t *testing.T, duringAck bool) {
	t.Helper()
	s, f, p, g, _ := imageService(t)
	g.Profile = policy.ProfileConsented
	saveGrant(t, p, g)
	f.items[0].Forwarded = true
	f.items[0].Message.Forward = &model.Forward{Date: "2026-09-01T00:00:00Z", FromName: "original"}
	r, err := s.Search(context.Background(), "req_forward_image", g.Peer, "caption", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	var e model.Envelope[model.SearchHit]
	if err := json.Unmarshal(r.JSON, &e); err != nil {
		t.Fatal(err)
	}
	change := func() { f.items[0].Message.Forward.FromName = "changed" }
	wantAck := 0
	if duringAck {
		f.onAck = change
		wantAck = 1
	} else {
		f.onDownload = change
	}
	r, err = s.OpenImage(context.Background(), "req_forward_changed", e.Items[0].Image.Handle)
	if err == nil || r.Image != nil || f.ackCalls != wantAck {
		t.Fatal("changed attribution released bytes")
	}
	for _, b := range f.data {
		if b != 0 {
			t.Fatal("failed download retained bytes")
		}
	}
}
