package reader

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

func refreshedEnvelope(t *testing.T, result Result, err error) model.Envelope[model.Message] {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.Envelope[model.Message]
	if err := json.Unmarshal(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestRefreshRecognizesEditsAndKeepsObservationsContentFree(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "private synthetic body")}
	result, err := s.Messages(context.Background(), "req_observe", request(g))
	page := refreshedEnvelope(t, result, err)
	token := page.Items[0].Observation
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil || strings.Contains(string(payload), "private synthetic") {
		t.Fatal("observation contains message text")
	}
	result, err = s.Refresh(context.Background(), "req_refresh_same", []string{token})
	page = refreshedEnvelope(t, result, err)
	if page.Items[0].RefreshState != "unchanged" || page.Items[0].Text != f.items[0].Message.Text || page.ReadEffect.Kind != model.ReadEffectHistoryMarkedRead || len(page.Contexts) != 1 {
		t.Fatal("unchanged refresh contract")
	}
	f.items[0].Message.Text = "edited body"
	f.items[0].Message.EditedAt = "2026-09-05T12:00:01Z"
	result, err = s.Refresh(context.Background(), "req_refresh_changed", []string{token})
	page = refreshedEnvelope(t, result, err)
	if page.Items[0].RefreshState != "changed" || page.Items[0].EditedAt == "" {
		t.Fatal("edit not observed")
	}
	result, err = s.Refresh(context.Background(), "req_refresh_current", []string{page.Items[0].Observation})
	page = refreshedEnvelope(t, result, err)
	if page.Items[0].RefreshState != "unchanged" {
		t.Fatal("fresh observation not reusable")
	}
}

func TestRefreshDistinguishesMediaIdentityFromTemporaryHandles(t *testing.T) {
	s, f, _, g, _ := documentService(t, "application/pdf")
	result, err := s.Messages(context.Background(), "req_media_observe", request(g))
	page := refreshedEnvelope(t, result, err)
	token := page.Items[0].Observation
	now := s.now()
	s.now = func() time.Time { return now.Add(time.Minute) }
	result, err = s.Refresh(context.Background(), "req_media_same", []string{token})
	page = refreshedEnvelope(t, result, err)
	if page.Items[0].RefreshState != "unchanged" {
		t.Fatal("new media handle looked changed")
	}
	f.items[0].Document.Fingerprint = strings.Repeat("c", 64)
	result, err = s.Refresh(context.Background(), "req_media_replaced", []string{token})
	page = refreshedEnvelope(t, result, err)
	if page.Items[0].RefreshState != "changed" || f.downloads != 0 {
		t.Fatal("replacement identity not observed")
	}
}

func TestRefreshRejectsInvalidAuthorityBeforeFetch(t *testing.T) {
	for _, variant := range []string{"tamper", "expired", "policy", "duplicate"} {
		t.Run(variant, func(t *testing.T) {
			s, f, p, _, g := testService(t)
			saveGrant(t, p, g)
			f.items = []model.Candidate{candidate(g, 20, "body")}
			result, err := s.Messages(context.Background(), "req_observe", request(g))
			page := refreshedEnvelope(t, result, err)
			tokens := []string{page.Items[0].Observation}
			switch variant {
			case "tamper":
				tokens[0] += "x"
			case "expired":
				now := s.now()
				s.now = func() time.Time { return now.Add(25 * time.Hour) }
			case "policy":
				saveGrant(t, p, g)
			case "duplicate":
				tokens = append(tokens, tokens[0])
			}
			f.historyCalls = 0
			f.ackCalls = 0
			result, err = s.Refresh(context.Background(), "req_invalid_observation", tokens)
			if err == nil || len(result.JSON) != 0 || f.historyCalls != 0 || f.ackCalls != 0 {
				t.Fatal("invalid observation fetched or released", err)
			}
		})
	}
}

func TestRefreshDoesNotInferDeletionOrReleaseOtherTargets(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "body")}
	result, err := s.Messages(context.Background(), "req_observe", request(g))
	page := refreshedEnvelope(t, result, err)
	f.items = nil
	f.ackCalls = 0
	result, err = s.Refresh(context.Background(), "req_missing", []string{page.Items[0].Observation})
	if model.TextErrorCategory(err) != model.ErrorPolicyDenied || len(result.JSON) != 0 || f.ackCalls != 0 {
		t.Fatal("missing target fabricated state", err)
	}
}

func TestRefreshBatchWithholdsPreparedTargetsOnMissingLaterMessage(t *testing.T) {
	s, f, p, _, g := testService(t)
	saveGrant(t, p, g)
	b := &batchBackend{fakeBackend: f, rows: map[model.PeerID][]model.Candidate{g.Peer: {candidate(g, 20, "first"), candidate(g, 21, "second")}}}
	s.backend = b
	result, err := s.Contexts(context.Background(), "req_observe_batch", []model.HistoryQuery{{Peer: g.Peer, Target: 20, Limit: 1}, {Peer: g.Peer, Target: 21, Limit: 1}})
	page := refreshedEnvelope(t, result, err)
	tokens := []string{page.Items[1].Observation, page.Items[0].Observation}
	b.rows[g.Peer] = b.rows[g.Peer][:1]
	b.ackCalls = 0
	result, err = s.Refresh(context.Background(), "req_missing_batch", tokens)
	if model.TextErrorCategory(err) != model.ErrorPolicyDenied || len(result.JSON) != 0 || b.ackCalls != 0 {
		t.Fatal("part of failed refresh batch escaped", err)
	}
}

func TestRefreshExpiryDuringSuccessfulAuditWithholdsBodies(t *testing.T) {
	s, f, p, db, g := testService(t)
	saveGrant(t, p, g)
	f.items = []model.Candidate{candidate(g, 20, "body")}
	result, err := s.Messages(context.Background(), "req_observe", request(g))
	page := refreshedEnvelope(t, result, err)
	observation, err := s.decodeObservation(page.Items[0].Observation)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	auditTime := now
	observation.Expires = now.Add(time.Second).Unix()
	token, err := s.encodeObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	f.ackCalls = 0
	s.now = func() time.Time { return now }
	hook := false
	repository, err := policy.New(db, filepath.Join(t.TempDir(), "state", "policy.lock"), func() time.Time {
		if f.ackCalls > 0 && !hook {
			hook = true
			now = now.Add(2 * time.Second)
		}
		return auditTime
	})
	if err != nil {
		t.Fatal(err)
	}
	s.policy = repository
	result, err = s.Refresh(context.Background(), "req_refresh_audit", []string{token})
	if !hook || f.ackCalls != 1 || model.TextErrorCategory(err) != model.ErrorReadEffectUncertain || len(result.JSON) != 0 {
		t.Fatal("expired observation released after audit", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM text_audit WHERE request_id='req_refresh_audit'").Scan(&count); err != nil || count != 1 {
		t.Fatal("audit fixture did not succeed", err)
	}
}
