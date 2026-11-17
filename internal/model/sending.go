package model

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	SendDispatchWindow   = 24 * time.Hour
	SendFutureSkew       = time.Minute
	SendRetention        = 7 * 24 * time.Hour
	MaximumSendTextUTF16 = 4096
)

var ErrOperationExpired = errors.New("send operation dispatch window expired")

// ErrSendMetadataRecording marks a failed adapter metadata write whose bounded
// cleanup grace has already been consumed. Callers must not start another one.
var ErrSendMetadataRecording = errors.New("send metadata recording failed")

// ResolveRecipientQuery is transient selection input, never stored authority.
type ResolveRecipientQuery struct {
	Username string `json:"username,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Peer     PeerID `json:"peer,omitempty"`
}

func (q ResolveRecipientQuery) Normalize() (ResolveRecipientQuery, error) {
	count := 0
	if q.Username != "" {
		count++
		q.Username = strings.TrimPrefix(q.Username, "@")
		if !linkUsername.MatchString(q.Username) {
			return ResolveRecipientQuery{}, TextError(ErrorInvalidInput, nil)
		}
	}
	if q.Phone != "" {
		count++
		if len(q.Phone) < 3 || len(q.Phone) > 16 || q.Phone[0] != '+' || q.Phone[1] < '1' || q.Phone[1] > '9' {
			return ResolveRecipientQuery{}, TextError(ErrorInvalidInput, nil)
		}
		for _, r := range q.Phone[2:] {
			if r < '0' || r > '9' {
				return ResolveRecipientQuery{}, TextError(ErrorInvalidInput, nil)
			}
		}
	}
	if q.Peer != (PeerID{}) {
		count++
		if !q.Peer.valid() {
			return ResolveRecipientQuery{}, TextError(ErrorInvalidInput, nil)
		}
	}
	if count != 1 {
		return ResolveRecipientQuery{}, TextError(ErrorInvalidInput, nil)
	}
	return q, nil
}
func (q ResolveRecipientQuery) Validate() error { _, err := q.Normalize(); return err }

type ResolvedRecipient struct {
	Peer        PeerID   `json:"peer"`
	Kind        PeerKind `json:"kind"`
	DisplayName string   `json:"display_name,omitempty"`
	Username    string   `json:"username,omitempty"`
	OperationID string   `json:"operation_id,omitempty"`
}

func (r ResolvedRecipient) Validate() error {
	if !r.Peer.valid() || r.Kind != r.Peer.Kind() || !utf8.ValidString(r.DisplayName) || utf8.RuneCountInString(r.DisplayName) > 256 {
		return errors.New("invalid resolved recipient")
	}
	for _, c := range r.DisplayName {
		if unicode.IsControl(c) {
			return errors.New("invalid recipient display metadata")
		}
	}
	if r.Username != "" && !linkUsername.MatchString(r.Username) {
		return errors.New("invalid recipient username")
	}
	if r.OperationID != "" {
		_, err := ParseOperationID(r.OperationID)
		return err
	}
	return nil
}

// SendRequest preserves text exactly; it never represents a queued operation.
type SendRequest struct {
	Peer        PeerID     `json:"peer"`
	Text        string     `json:"text"`
	OperationID string     `json:"operation_id"`
	ReplyTo     *MessageID `json:"reply_to,omitempty"`
}

func (r SendRequest) Validate() error {
	if !r.Peer.valid() {
		return TextError(ErrorInvalidInput, nil)
	}
	if _, err := ParseOperationID(r.OperationID); err != nil {
		return TextError(ErrorInvalidInput, nil)
	}
	if !utf8.ValidString(r.Text) || strings.TrimSpace(r.Text) == "" || len(utf16.Encode([]rune(r.Text))) > MaximumSendTextUTF16 {
		return TextError(ErrorInvalidInput, nil)
	}
	if r.ReplyTo != nil && (!r.ReplyTo.valid() || r.ReplyTo.Peer() != r.Peer) {
		return TextError(ErrorInvalidReference, nil)
	}
	return nil
}

type SendState string

const (
	SendNotSent   SendState = "not_sent"
	SendInFlight  SendState = "in_flight"
	SendSent      SendState = "sent"
	SendUncertain SendState = "uncertain"
)

func (s SendState) IsValid() bool {
	return s == SendNotSent || s == SendInFlight || s == SendSent || s == SendUncertain
}

type SendFailureCode string

const (
	SendFailureWriteForbidden       SendFailureCode = "write_forbidden"
	SendFailurePaymentRequired      SendFailureCode = "payment_required"
	SendFailureRateLimited          SendFailureCode = "rate_limited"
	SendFailureAccountRestricted    SendFailureCode = "account_restricted"
	SendFailureEffectUncertain      SendFailureCode = "send_effect_uncertain"
	SendFailureStateSyncFailed      SendFailureCode = "state_sync_failed"
	SendFailureReceiptNotPersisted  SendFailureCode = "receipt_not_persisted"
	SendFailureRecipientUnavailable SendFailureCode = "recipient_unavailable"
	SendFailureInvalidReference     SendFailureCode = "invalid_reference"
	SendFailureMessageTooLong       SendFailureCode = "message_too_long"
)

func (c SendFailureCode) IsValid() bool {
	switch c {
	case SendFailureWriteForbidden, SendFailurePaymentRequired, SendFailureRateLimited, SendFailureAccountRestricted, SendFailureEffectUncertain, SendFailureStateSyncFailed, SendFailureReceiptNotPersisted, SendFailureRecipientUnavailable, SendFailureInvalidReference, SendFailureMessageTooLong:
		return true
	}
	return false
}

type SendOperation struct {
	OperationID string          `json:"operation_id"`
	Peer        PeerID          `json:"peer"`
	State       SendState       `json:"state"`
	MessageID   *MessageID      `json:"message_id,omitempty"`
	SentAt      string          `json:"sent_at,omitempty"`
	FailureCode SendFailureCode `json:"failure_code,omitempty"`
	RetryAt     string          `json:"retry_at,omitempty"`
}

func (o SendOperation) Validate() error {
	if _, err := ParseOperationID(o.OperationID); err != nil {
		return err
	}
	if !o.Peer.valid() || !o.State.IsValid() {
		return errors.New("invalid send operation")
	}
	if o.State == SendSent {
		if o.MessageID == nil || !o.MessageID.valid() || o.MessageID.Peer() != o.Peer {
			return errors.New("sent operation requires a matching receipt")
		}
	} else if o.MessageID != nil || o.SentAt != "" {
		return errors.New("unconfirmed operation cannot carry a receipt")
	}
	if o.SentAt != "" && !validSendingTime(o.SentAt) {
		return errors.New("invalid send timestamp")
	}
	if o.FailureCode != "" && !o.FailureCode.IsValid() {
		return errors.New("invalid send failure code")
	}
	if o.RetryAt != "" && (o.FailureCode != SendFailureRateLimited || !validSendingTime(o.RetryAt)) {
		return errors.New("invalid send retry timestamp")
	}
	if o.FailureCode == SendFailureRateLimited && o.RetryAt == "" {
		return errors.New("rate limited operation requires a retry timestamp")
	}
	return nil
}

// SendAttempt is internal backend input. Telegram random IDs never enter MCP.
type SendAttempt struct {
	OperationID string
	Peer        PeerID
	Text        string
	ReplyTo     *MessageID
	RandomID    int64
}

func (a SendAttempt) Validate() error {
	if a.RandomID == 0 {
		return errors.New("send random ID is required")
	}
	return (SendRequest{Peer: a.Peer, Text: a.Text, OperationID: a.OperationID, ReplyTo: a.ReplyTo}).Validate()
}

type SendReceipt struct {
	Peer      PeerID
	RandomID  int64
	MessageID MessageID
	SentAt    string
}

func (r SendReceipt) Validate() error {
	if !r.Peer.valid() || r.RandomID == 0 || !r.MessageID.valid() || r.MessageID.Peer() != r.Peer || (r.SentAt != "" && !validSendingTime(r.SentAt)) {
		return errors.New("invalid send receipt")
	}
	return nil
}

type SendAttemptResult struct {
	Receipt     *SendReceipt
	FailureCode SendFailureCode
	RetryAt     time.Time
	Attempted   bool
	Uncertain   bool
}

// PreparedSend contains a validated adapter-owned request. Calling it may
// initiate exactly one send; construction performs only recipient preflight.
type PreparedSend func(context.Context) (SendAttemptResult, error)

func NewOperationID(now time.Time, random io.Reader) (string, error) {
	if now.IsZero() || now.UnixMilli() < 0 || now.UnixMilli()>>48 != 0 || random == nil {
		return "", errors.New("invalid operation ID source")
	}
	var id uuid.UUID
	n := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		id[i] = byte(n)
		n >>= 8
	}
	if _, err := io.ReadFull(random, id[6:]); err != nil {
		return "", &SendError{Category: ErrorInternal, Cause: err}
	}
	id[6] = (id[6] & 15) | 0x70
	id[8] = (id[8] & 63) | 0x80
	return id.String(), nil
}
func ParseOperationID(value string) (time.Time, error) {
	if len(value) != 36 {
		return time.Time{}, errors.New("invalid send operation ID")
	}
	id, err := uuid.Parse(value)
	if err != nil || id.String() != value || id.Version() != 7 || id.Variant() != uuid.RFC4122 {
		return time.Time{}, errors.New("invalid send operation ID")
	}
	var millis int64
	for _, b := range id[:6] {
		millis = (millis << 8) | int64(b)
	}
	return time.UnixMilli(millis).UTC(), nil
}
func ValidateOperationWindow(value string, now time.Time) error {
	created, err := ParseOperationID(value)
	if err != nil {
		return err
	}
	if now.IsZero() {
		return errors.New("send operation clock is required")
	}
	if created.After(now.Add(SendFutureSkew)) || !now.Before(created.Add(SendDispatchWindow)) {
		return ErrOperationExpired
	}
	return nil
}
func validSendingTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero() && strings.HasSuffix(value, "Z")
}

// SendError exposes only fixed failure metadata. The private cause is never
// serialized or included in its error text.
type SendError struct {
	Category    ErrorCategory
	FailureCode SendFailureCode
	RetryAt     time.Time
	// MetadataDeadline bounds the shared local recording grace. It never
	// describes a provider wait or enters a public result.
	MetadataDeadline time.Time `json:"-"`
	Cause            error
}

func (e *SendError) Error() string                         { return "send operation failed" }
func (e *SendError) Unwrap() error                         { return e.Cause }
func (e *SendError) operationErrorCategory() ErrorCategory { return e.Category }
