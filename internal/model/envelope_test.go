package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeUsesVersionedPrecisionSafeShape(t *testing.T) {
	t.Parallel()

	peer, err := ParsePeerID("tgpeer:v1:user:9007199254740992")
	if err != nil {
		t.Fatal(err)
	}
	freshness, err := NewFreshness(FreshnessLive, time.Date(2026, 9, 4, 12, 30, 0, 123, time.FixedZone("offset", 3*60*60)))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := NewEnvelope("req_envelope_1", freshness, []PeerID{peer})
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	for _, expected := range []string{
		`"schema_version":"1"`,
		`"checked_at":"2026-09-04T09:30:00.000000123Z"`,
		`"items":["tgpeer:v1:user:9007199254740992"]`,
		`"warnings":[]`,
		`"untrusted_content":true`,
	} {
		if !strings.Contains(jsonText, expected) {
			t.Fatalf("JSON %s does not contain %s", jsonText, expected)
		}
	}
	if strings.Contains(jsonText, `"items":[9007199254740992]`) {
		t.Fatalf("identifier crossed JSON as a number: %s", jsonText)
	}

	var decoded Envelope[PeerID]
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped envelope is invalid: %v", err)
	}
	if len(decoded.Items) != 1 || decoded.Items[0] != peer {
		t.Fatalf("round-tripped items = %#v, want %#v", decoded.Items, []PeerID{peer})
	}
}

func TestEnvelopeRejectsInvalidContracts(t *testing.T) {
	t.Parallel()

	freshness, err := NewFreshness(FreshnessLive, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewEnvelope[PeerID]("not-a-request-id", freshness, nil); err == nil {
		t.Fatal("NewEnvelope() accepted invalid request ID")
	}
	if _, err := NewFreshness(FreshnessState("fresh-ish"), time.Now()); err == nil {
		t.Fatal("NewFreshness() accepted invalid state")
	}
	if err := ValidatePageSize(0); err == nil {
		t.Fatal("ValidatePageSize() accepted zero")
	}
	if err := ValidatePageSize(MaximumPageSize + 1); err == nil {
		t.Fatal("ValidatePageSize() accepted value above hard maximum")
	}
	if _, err := NewEnvelope("req_too_many", freshness, make([]PeerID, MaximumPageSize+1)); err == nil {
		t.Fatal("NewEnvelope() accepted too many items")
	}
}

func TestReadEffectRequiresMessageReference(t *testing.T) {
	t.Parallel()

	if err := (ReadEffect{Kind: ReadEffectHistoryMarkedRead}).Validate(); err == nil {
		t.Fatal("state-affecting read effect accepted without a message reference")
	}
	message, err := ParseMessageID("tgmsg:v1:user:1:2")
	if err != nil {
		t.Fatal(err)
	}
	if err := (ReadEffect{Kind: ReadEffectHistoryMarkedRead, ThroughMessageID: &message}).Validate(); err != nil {
		t.Fatalf("valid read effect rejected: %v", err)
	}
}

func TestErrorEnvelopeUsesFixedContentFreeMessage(t *testing.T) {
	t.Parallel()

	retry := uint32(7)
	envelope, err := NewErrorEnvelope(ErrorRateLimited, "req_rate_1", &retry)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "RPC") || !strings.Contains(string(encoded), `"retry_after_seconds":7`) {
		t.Fatalf("unexpected error envelope: %s", encoded)
	}
	if _, err := NewErrorEnvelope(ErrorInternal, "req_internal_1", &retry); err == nil {
		t.Fatal("non-rate error accepted a retry delay")
	}
	if _, err := NewErrorEnvelope(ErrorRateLimited, "req_rate_2", nil); err == nil {
		t.Fatal("rate-limited error accepted a missing retry delay")
	}
	retry = MaximumRetryAfterSeconds + 1
	if _, err := NewErrorEnvelope(ErrorRateLimited, "req_rate_3", &retry); err == nil {
		t.Fatal("rate-limited error accepted an excessive retry delay")
	}
}

func TestDecodeStrictRejectsUnknownAndTrailingFields(t *testing.T) {
	t.Parallel()

	type request struct {
		Limit int `json:"limit"`
	}
	decoded, err := DecodeStrict[request]([]byte(`{"limit":20}`))
	if err != nil || decoded.Limit != 20 {
		t.Fatalf("DecodeStrict(valid) = %#v, %v", decoded, err)
	}
	if _, err := DecodeStrict[request]([]byte(`{"limit":20,"peer":"hidden"}`)); err == nil {
		t.Fatal("DecodeStrict() accepted unknown field")
	}
	if _, err := DecodeStrict[request]([]byte(`{"limit":20} {"limit":30}`)); err == nil {
		t.Fatal("DecodeStrict() accepted trailing value")
	}
	if _, err := DecodeStrict[request]([]byte(`null`)); err == nil {
		t.Fatal("DecodeStrict() accepted a non-object input")
	}
}

func TestEnvelopeMarshalEnforcesCodesCursorAndByteBudget(t *testing.T) {
	t.Parallel()

	freshness, err := NewFreshness(FreshnessLive, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := NewEnvelope("req_budget_1", freshness, []string{"ok"})
	if err != nil {
		t.Fatal(err)
	}
	envelope.Warnings = []WarningCode{WarningCode("free-form warning")}
	if _, err := json.Marshal(envelope); err == nil {
		t.Fatal("Marshal() accepted a free-form warning")
	}
	envelope.Warnings = []WarningCode{WarningPartialResult}
	invalidCursor := "cursor with spaces"
	envelope.NextCursor = &invalidCursor
	if _, err := json.Marshal(envelope); err == nil {
		t.Fatal("Marshal() accepted an invalid cursor")
	}
	envelope.NextCursor = nil
	envelope.Items = []string{strings.Repeat("x", MaximumTextResultBytes)}
	if _, err := json.Marshal(envelope); err == nil {
		t.Fatal("Marshal() accepted a result above the byte budget")
	}
}

func TestEnvelopeEmptyAndCopiedResults(t *testing.T) {
	freshness, err := NewFreshness(FreshnessLive, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, items := range [][]string{nil, {}, {"original"}} {
		envelope, err := NewEnvelope("req_result", freshness, items)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			items[0] = "modified"
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 && !strings.Contains(string(encoded), `"items":[]`) {
			t.Fatalf("empty result is not an array: %s", encoded)
		}
		if strings.Contains(string(encoded), "modified") {
			t.Fatal("constructor retained caller-owned slice")
		}
	}
}

func TestScopeCoverageRejectsInconsistentCounts(t *testing.T) {
	id, err := ParseScopeID("tgscope:v1:0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	valid := ScopeCoverage{ID: id, TotalPeers: 3, EligiblePeers: 2, ExcludedPeers: 1, QueriedPeers: 1, CompletedPeers: 2}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ScopeCoverage){
		func(c *ScopeCoverage) { c.ID = "invalid" },
		func(c *ScopeCoverage) { c.TotalPeers = 21 },
		func(c *ScopeCoverage) { c.TotalPeers = -1 },
		func(c *ScopeCoverage) { c.ExcludedPeers = 0 },
		func(c *ScopeCoverage) { c.EligiblePeers = -1 },
		func(c *ScopeCoverage) { c.QueriedPeers = 3 },
		func(c *ScopeCoverage) { c.CompletedPeers = 3 },
		func(c *ScopeCoverage) { c.QueriedPeers = -1 },
		func(c *ScopeCoverage) { c.CompletedPeers = -1 },
	} {
		invalid := valid
		change(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatal("invalid scope coverage accepted")
		}
	}
}
