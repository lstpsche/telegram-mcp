package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func sendingFixture(t *testing.T) (time.Time, string, PeerID) {
	t.Helper()
	now := time.Date(2026, 11, 17, 12, 0, 0, 123000000, time.UTC)
	id, err := NewOperationID(now, bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := NewPeerID(PeerKindUser, 42)
	if err != nil {
		t.Fatal(err)
	}
	return now, id, peer
}

func TestOperationIDIsCanonicalVersionSevenWithBoundedDispatchWindow(t *testing.T) {
	now, id, _ := sendingFixture(t)
	if !strings.HasSuffix(id, "-7000-8000-000000000000") {
		t.Fatal("version or variant was not fixed", id)
	}

	created, err := ParseOperationID(id)
	if err != nil || !created.Equal(now) {
		t.Fatal(created, err)
	}
	for _, at := range []time.Time{now.Add(-SendFutureSkew), now.Add(SendDispatchWindow - time.Nanosecond)} {
		if err := ValidateOperationWindow(id, at); err != nil {
			t.Fatal("valid boundary rejected", err)
		}
	}
	for _, at := range []time.Time{now.Add(-SendFutureSkew - time.Nanosecond), now.Add(SendDispatchWindow)} {
		if !errors.Is(ValidateOperationWindow(id, at), ErrOperationExpired) {
			t.Fatal("expired boundary accepted")
		}
	}
	if _, err := ParseOperationID(strings.ToUpper(id)); err == nil {
		t.Fatal("uppercase ID accepted")
	}
	for _, bad := range []string{"", strings.Replace(id, "-7000-", "-4000-", 1), strings.Replace(id, "-8000-", "-c000-", 1), "{" + id + "}", strings.ReplaceAll(id, "-", "")} {
		if _, err := ParseOperationID(bad); err == nil {
			t.Fatal("noncanonical ID accepted")
		}
	}
	if _, err := ParseOperationID(id); err != nil {
		t.Fatal("syntax inspection must remain valid outside dispatch window", err)
	}
	if _, err := NewOperationID(now, io.LimitReader(bytes.NewReader(make([]byte, 10)), 9)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("randomness failure cause lost", err)
	}
	if _, err := NewOperationID(time.Time{}, bytes.NewReader(make([]byte, 10))); err == nil {
		t.Fatal("empty clock accepted")
	}
	if err := ValidateOperationWindow(id, time.Time{}); err == nil {
		t.Fatal("empty dispatch clock accepted")
	}
}

func TestResolveRecipientQueryHasOneExactSelector(t *testing.T) {
	_, _, peer := sendingFixture(t)
	for _, q := range []ResolveRecipientQuery{{Username: "a"}, {Username: "@Assigned_Name"}, {Phone: "+12025550123"}, {Peer: peer}} {
		if err := q.Validate(); err != nil {
			t.Fatal("valid selector rejected", err)
		}
	}
	normalized, err := (ResolveRecipientQuery{Username: "@Assigned_Name"}).Normalize()
	if err != nil || normalized.Username != "Assigned_Name" {
		t.Fatal("username normalization", err)
	}
	for _, q := range []ResolveRecipientQuery{{}, {Username: "@@name"}, {Username: " name"}, {Username: "https://t.me/name"}, {Username: "name", Phone: "+12025550123"}, {Peer: peer, Phone: "+12025550123"}, {Phone: "12025550123"}, {Phone: "+0123"}, {Phone: "+1"}, {Phone: "+1 2025550123"}, {Phone: "+1234567890123456"}} {
		if q.Validate() == nil {
			t.Fatal("invalid selector accepted")
		}
	}
}

func TestSendRequestPreservesTextAndCountsUTF16(t *testing.T) {
	_, id, peer := sendingFixture(t)
	text := "  exact\ttext\r\nwith \U0001f642  "
	request := SendRequest{Peer: peer, Text: text, OperationID: id}
	if err := request.Validate(); err != nil || request.Text != text {
		t.Fatal("original text changed", err)
	}
	request.Text = strings.Repeat("\U0001f642", MaximumSendTextUTF16/2)
	if err := request.Validate(); err != nil {
		t.Fatal("UTF16 boundary rejected", err)
	}
	request.Text += "x"
	if request.Validate() == nil {
		t.Fatal("UTF16 overflow accepted")
	}
	for _, value := range []string{"", " \n\r\t", string([]byte{0xff})} {
		request.Text = value
		if request.Validate() == nil {
			t.Fatal("invalid text accepted")
		}
	}
	request.Text = text
	other, _ := NewPeerID(PeerKindChat, 42)
	reply, _ := NewMessageID(other, 1)
	request.ReplyTo = &reply
	if TextErrorCategory(request.Validate()) != ErrorInvalidReference {
		t.Fatal("cross-peer reply accepted")
	}
	reply, _ = NewMessageID(peer, 1)
	request.ReplyTo = &reply
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSendOperationsOnlyExposeValidatedFacts(t *testing.T) {
	now, id, peer := sendingFixture(t)
	message, _ := NewMessageID(peer, 9)
	operation := SendOperation{OperationID: id, Peer: peer, State: SendSent, MessageID: &message, SentAt: now.Format(time.RFC3339Nano)}
	if err := operation.Validate(); err != nil {
		t.Fatal(err)
	}
	operation.FailureCode = SendFailureStateSyncFailed
	if err := operation.Validate(); err != nil {
		t.Fatal("confirmed receipt lost on synchronization failure", err)
	}
	operation.State = SendUncertain
	if operation.Validate() == nil {
		t.Fatal("unconfirmed receipt accepted")
	}
	operation.MessageID = nil
	operation.SentAt = ""
	operation.FailureCode = SendFailureRateLimited
	if operation.Validate() == nil {
		t.Fatal("rate failure without deadline accepted")
	}
	operation.RetryAt = now.Add(48 * time.Hour).Format(time.RFC3339Nano)
	if err := operation.Validate(); err != nil {
		t.Fatal("long provider wait rejected", err)
	}
	operation.FailureCode = "sensitive provider detail"
	if operation.Validate() == nil {
		t.Fatal("open failure code accepted")
	}
	recipient := ResolvedRecipient{Peer: peer, Kind: peer.Kind()}
	if err := recipient.Validate(); err != nil {
		t.Fatal("unknown display metadata should be absent", err)
	}
	recipient.DisplayName = "hostile\nmetadata"
	if recipient.Validate() == nil {
		t.Fatal("control-bearing display metadata accepted")
	}
	receipt := SendReceipt{Peer: peer, RandomID: 7, MessageID: message}
	if err := receipt.Validate(); err != nil {
		t.Fatal("receipt without provider date rejected", err)
	}
	receipt.RandomID = 0
	if receipt.Validate() == nil {
		t.Fatal("zero random ID accepted")
	}
}

func TestAbsoluteRetryErrorsPreserveLongProviderWaitsAndLegacyShape(t *testing.T) {
	now, _, _ := sendingFixture(t)
	retryAt := now.Add(48 * time.Hour)
	absolute, err := NewRetryAtErrorEnvelope("req_send", retryAt)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(absolute)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"retry_at":"`+retryAt.Format(time.RFC3339Nano)+`"`)) || bytes.Contains(encoded, []byte("retry_after_seconds")) {
		t.Fatal("absolute retry altered", string(encoded))
	}
	seconds := uint32(2)
	legacy, err := NewErrorEnvelope(ErrorRateLimited, "req_read", &seconds)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(legacy)
	if err != nil || !bytes.Contains(encoded, []byte(`"retry_after_seconds":2`)) || bytes.Contains(encoded, []byte("retry_at")) {
		t.Fatal("legacy retry shape changed", err)
	}
	absolute.Error.RetryAfterSeconds = &seconds
	if absolute.Validate() == nil {
		t.Fatal("two retry timing forms accepted")
	}
	absolute.Error.RetryAfterSeconds = nil
	absolute.Error.Category = ErrorInternal
	if absolute.Validate() == nil {
		t.Fatal("nonrate deadline accepted")
	}
	if _, err := NewRetryAtErrorEnvelope("req_send", time.Time{}); err == nil {
		t.Fatal("zero deadline accepted")
	}
}

func TestFailedSendEnvelopeKeepsKnownReceiptAndFixedError(t *testing.T) {
	now, id, peer := sendingFixture(t)
	message, _ := NewMessageID(peer, 9)
	fresh, _ := NewFreshness(FreshnessLive, now)
	envelope, err := NewEnvelope("req_send", fresh, []SendOperation{{OperationID: id, Peer: peer, State: SendSent, MessageID: &message, FailureCode: SendFailureReceiptNotPersisted}})
	if err != nil {
		t.Fatal(err)
	}
	failure, _ := NewErrorEnvelope(ErrorReceiptNotPersisted, "req_send", nil)
	envelope.Error = &failure.Error
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"state":"sent"`)) || !bytes.Contains(encoded, []byte(`"error":{"category":"receipt_not_persisted"`)) {
		t.Fatal("known receipt or fixed error lost", string(encoded))
	}
	cause := errors.New("sensitive provider text")
	sendErr := &SendError{Category: ErrorRateLimited, FailureCode: SendFailureRateLimited, RetryAt: now, Cause: cause}
	if errors.Unwrap(sendErr) != cause || strings.Contains(sendErr.Error(), cause.Error()) || TextErrorCategory(sendErr) != ErrorRateLimited {
		t.Fatal("send error leaked or discarded private cause")
	}
}

func TestTextErrorCategoryPreservesTheResponsibleStructuredBoundary(t *testing.T) {
	rate := &SendError{Category: ErrorRateLimited, FailureCode: SendFailureRateLimited}
	metadataFailure := errors.New("synthetic metadata failure")
	outerOperation := TextError(ErrorInternal, errors.Join(rate, metadataFailure))
	if category := TextErrorCategory(outerOperation); category != ErrorInternal {
		t.Fatal("nested send failure replaced responsible metadata failure", category)
	}
	outerSend := &SendError{Category: ErrorWriteForbidden, FailureCode: SendFailureWriteForbidden, Cause: TextError(ErrorInternal, metadataFailure)}
	if category := TextErrorCategory(outerSend); category != ErrorWriteForbidden {
		t.Fatal("nested operation failure replaced responsible send failure", category)
	}
	if category := TextErrorCategory(errors.Join(errors.New("unstructured context"), outerOperation)); category != ErrorInternal {
		t.Fatal("joined structured boundary was lost", category)
	}
	if !SendFailureMessageTooLong.IsValid() {
		t.Fatal("Telegram length rejection must have a fixed failure code")
	}
}
