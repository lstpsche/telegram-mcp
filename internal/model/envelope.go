package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	SchemaVersion            = "1"
	DefaultPageSize          = 20
	MaximumPageSize          = 100
	MaximumTextResultBytes   = 256 * 1024
	MaximumRetryAfterSeconds = 3600
	maximumRequestIDLen      = 128
	maximumCursorLength      = 4096
)

var requestIDPattern = regexp.MustCompile(`^req_[A-Za-z0-9_-]+$`)
var cursorPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type FreshnessState string

const (
	FreshnessLive        FreshnessState = "live"
	FreshnessRecovering  FreshnessState = "recovering"
	FreshnessStale       FreshnessState = "stale"
	FreshnessPartial     FreshnessState = "partial"
	FreshnessUnavailable FreshnessState = "unavailable"
)

func (s FreshnessState) IsValid() bool {
	switch s {
	case FreshnessLive, FreshnessRecovering, FreshnessStale, FreshnessPartial, FreshnessUnavailable:
		return true
	default:
		return false
	}
}

// Freshness states what was actually checked, with an always-UTC timestamp.
type Freshness struct {
	Telegram  FreshnessState `json:"telegram"`
	CheckedAt string         `json:"checked_at"`
}

func NewFreshness(state FreshnessState, checkedAt time.Time) (Freshness, error) {
	if !state.IsValid() {
		return Freshness{}, errors.New("invalid freshness state")
	}
	if checkedAt.IsZero() {
		return Freshness{}, errors.New("freshness timestamp is required")
	}
	return Freshness{
		Telegram:  state,
		CheckedAt: checkedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (f Freshness) Validate() error {
	if !f.Telegram.IsValid() {
		return errors.New("invalid freshness state")
	}
	parsed, err := time.Parse(time.RFC3339Nano, f.CheckedAt)
	if err != nil || !strings.HasSuffix(f.CheckedAt, "Z") || parsed.IsZero() {
		return errors.New("freshness timestamp must be RFC 3339 UTC")
	}
	return nil
}

type ReadEffectKind string

const (
	ReadEffectNone              ReadEffectKind = "none"
	ReadEffectHistoryMarkedRead ReadEffectKind = "history_marked_read"
	ReadEffectContentMarkedRead ReadEffectKind = "content_marked_read"
)

type ReadEffect struct {
	Kind             ReadEffectKind `json:"kind"`
	ThroughMessageID *MessageID     `json:"through_message_id,omitempty"`
}

func NoReadEffect() ReadEffect { return ReadEffect{Kind: ReadEffectNone} }

func (e ReadEffect) Validate() error {
	switch e.Kind {
	case ReadEffectNone:
		if e.ThroughMessageID != nil {
			return errors.New("none read effect cannot have a message reference")
		}
	case ReadEffectHistoryMarkedRead, ReadEffectContentMarkedRead:
		if e.ThroughMessageID == nil || !e.ThroughMessageID.valid() {
			return errors.New("state-affecting read effect requires a valid message reference")
		}
	default:
		return errors.New("invalid read effect")
	}
	return nil
}

// WarningCode is a stable machine code. It deliberately has no free-form text
// field that could carry Telegram content into logs or tool metadata.
type WarningCode string

const (
	WarningFreshnessDegraded WarningCode = "freshness_degraded"
	WarningPartialResult     WarningCode = "partial_result"
)

func (code WarningCode) valid() bool {
	switch code {
	case WarningFreshnessDegraded, WarningPartialResult:
		return true
	default:
		return false
	}
}

// Envelope is the shared v1 output shape for successful MCP results.
type Envelope[T any] struct {
	SchemaVersion    string        `json:"schema_version"`
	RequestID        string        `json:"request_id"`
	Freshness        Freshness     `json:"freshness"`
	Partial          bool          `json:"partial"`
	ReadEffect       ReadEffect    `json:"read_effect"`
	Items            []T           `json:"items"`
	NextCursor       *string       `json:"next_cursor"`
	Warnings         []WarningCode `json:"warnings"`
	UntrustedContent bool          `json:"untrusted_content"`
}

func NewEnvelope[T any](requestID string, freshness Freshness, items []T) (Envelope[T], error) {
	if err := validateRequestID(requestID); err != nil {
		return Envelope[T]{}, err
	}
	if err := freshness.Validate(); err != nil {
		return Envelope[T]{}, err
	}
	if len(items) > MaximumPageSize {
		return Envelope[T]{}, errors.New("result item count exceeds the server maximum")
	}
	items = append(make([]T, 0, len(items)), items...)
	return Envelope[T]{
		SchemaVersion:    SchemaVersion,
		RequestID:        requestID,
		Freshness:        freshness,
		ReadEffect:       NoReadEffect(),
		Items:            items,
		Warnings:         make([]WarningCode, 0),
		UntrustedContent: true,
	}, nil
}

func (e Envelope[T]) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return errors.New("unsupported schema version")
	}
	if err := validateRequestID(e.RequestID); err != nil {
		return err
	}
	if err := e.Freshness.Validate(); err != nil {
		return err
	}
	if err := e.ReadEffect.Validate(); err != nil {
		return err
	}
	if e.Items == nil || e.Warnings == nil {
		return errors.New("items and warnings must encode as arrays")
	}
	if len(e.Items) > MaximumPageSize {
		return errors.New("result item count exceeds the server maximum")
	}
	for _, warning := range e.Warnings {
		if !warning.valid() {
			return errors.New("invalid warning code")
		}
	}
	if e.NextCursor != nil && (len(*e.NextCursor) == 0 || len(*e.NextCursor) > maximumCursorLength || !cursorPattern.MatchString(*e.NextCursor)) {
		return errors.New("invalid continuation cursor")
	}
	if !e.UntrustedContent {
		return errors.New("untrusted-content marker must be true")
	}
	return nil
}

func (e Envelope[T]) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wireEnvelope[T any] Envelope[T]
	encoded, err := json.Marshal(wireEnvelope[T](e))
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaximumTextResultBytes {
		return nil, errors.New("serialized result exceeds the server byte budget")
	}
	return encoded, nil
}

type ErrorCategory string

const (
	ErrorInvalidInput        ErrorCategory = "invalid_input"
	ErrorNotReady            ErrorCategory = "not_ready"
	ErrorReauthRequired      ErrorCategory = "reauth_required"
	ErrorPolicyDenied        ErrorCategory = "policy_denied"
	ErrorConsentRequired     ErrorCategory = "consent_required"
	ErrorUnsupportedPeer     ErrorCategory = "unsupported_peer"
	ErrorProtectedContent    ErrorCategory = "protected_content"
	ErrorEphemeralContent    ErrorCategory = "ephemeral_content"
	ErrorInvalidReference    ErrorCategory = "invalid_reference"
	ErrorCursorInvalid       ErrorCategory = "cursor_invalid"
	ErrorCursorExpired       ErrorCategory = "cursor_expired"
	ErrorResourceExpired     ErrorCategory = "resource_expired"
	ErrorRateLimited         ErrorCategory = "rate_limited"
	ErrorFreshnessDegraded   ErrorCategory = "freshness_degraded"
	ErrorPartialResult       ErrorCategory = "partial_result"
	ErrorTelegramUnavailable ErrorCategory = "telegram_unavailable"
	ErrorResultTooLarge      ErrorCategory = "result_too_large"
	ErrorMediaTooLarge       ErrorCategory = "media_too_large"
	ErrorCancelled           ErrorCategory = "cancelled"
	ErrorInternal            ErrorCategory = "internal"
)

func (category ErrorCategory) message() (string, bool) {
	switch category {
	case ErrorInvalidInput:
		return "The supplied tool input is invalid", true
	case ErrorNotReady:
		return "Telegram MCP is not ready", true
	case ErrorReauthRequired:
		return "Telegram authorization must be renewed", true
	case ErrorPolicyDenied:
		return "The configured policy denied this operation", true
	case ErrorConsentRequired:
		return "A current consent grant is required", true
	case ErrorUnsupportedPeer:
		return "This peer type is not supported", true
	case ErrorProtectedContent:
		return "Protected content cannot be returned", true
	case ErrorEphemeralContent:
		return "Expiring content cannot be returned", true
	case ErrorInvalidReference:
		return "The supplied reference is invalid", true
	case ErrorCursorInvalid:
		return "The continuation cursor is invalid", true
	case ErrorCursorExpired:
		return "The continuation cursor has expired", true
	case ErrorResourceExpired:
		return "The resource handle has expired", true
	case ErrorRateLimited:
		return "Telegram asked the client to retry later", true
	case ErrorFreshnessDegraded:
		return "Required state freshness is degraded", true
	case ErrorPartialResult:
		return "Only a bounded partial result is available", true
	case ErrorTelegramUnavailable:
		return "Telegram is unavailable", true
	case ErrorResultTooLarge:
		return "The result exceeds the server budget", true
	case ErrorMediaTooLarge:
		return "The media exceeds the server budget", true
	case ErrorCancelled:
		return "The operation was cancelled", true
	case ErrorInternal:
		return "Telegram MCP could not complete the operation", true
	default:
		return "", false
	}
}

func (category ErrorCategory) IsValid() bool {
	_, valid := category.message()
	return valid
}

type ErrorBody struct {
	Category          ErrorCategory `json:"category"`
	Message           string        `json:"message"`
	RetryAfterSeconds *uint32       `json:"retry_after_seconds,omitempty"`
}

type ErrorEnvelope struct {
	SchemaVersion string    `json:"schema_version"`
	RequestID     string    `json:"request_id"`
	Error         ErrorBody `json:"error"`
}

func NewErrorEnvelope(category ErrorCategory, requestID string, retryAfter *uint32) (ErrorEnvelope, error) {
	message, ok := category.message()
	if !ok {
		return ErrorEnvelope{}, errors.New("invalid error category")
	}
	if err := validateRequestID(requestID); err != nil {
		return ErrorEnvelope{}, err
	}
	if category == ErrorRateLimited && retryAfter == nil {
		return ErrorEnvelope{}, errors.New("rate-limited errors require a retry delay")
	}
	if category != ErrorRateLimited && retryAfter != nil {
		return ErrorEnvelope{}, errors.New("retry delay is only valid for rate-limited errors")
	}
	if retryAfter != nil && (*retryAfter == 0 || *retryAfter > MaximumRetryAfterSeconds) {
		return ErrorEnvelope{}, errors.New("retry delay is outside the allowed range")
	}
	var retryAfterCopy *uint32
	if retryAfter != nil {
		value := *retryAfter
		retryAfterCopy = &value
	}
	return ErrorEnvelope{
		SchemaVersion: SchemaVersion,
		RequestID:     requestID,
		Error: ErrorBody{
			Category:          category,
			Message:           message,
			RetryAfterSeconds: retryAfterCopy,
		},
	}, nil
}

func (e ErrorEnvelope) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return errors.New("unsupported schema version")
	}
	if err := validateRequestID(e.RequestID); err != nil {
		return err
	}
	message, valid := e.Error.Category.message()
	if !valid || e.Error.Message != message {
		return errors.New("invalid error body")
	}
	if e.Error.Category == ErrorRateLimited {
		if e.Error.RetryAfterSeconds == nil || *e.Error.RetryAfterSeconds == 0 || *e.Error.RetryAfterSeconds > MaximumRetryAfterSeconds {
			return errors.New("rate-limited error has an invalid retry delay")
		}
	} else if e.Error.RetryAfterSeconds != nil {
		return errors.New("retry delay is only valid for rate-limited errors")
	}
	return nil
}

func (e ErrorEnvelope) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wireErrorEnvelope ErrorEnvelope
	return json.Marshal(wireErrorEnvelope(e))
}

func ValidatePageSize(size int) error {
	if size < 1 || size > MaximumPageSize {
		return fmt.Errorf("page size must be between 1 and %d", MaximumPageSize)
	}
	return nil
}

// DecodeStrict rejects unknown fields and trailing JSON values. It is the
// required input boundary for future MCP request structs.
func DecodeStrict[T any](data []byte) (T, error) {
	var value T
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return value, errors.New("decode input: top-level JSON value must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("decode input: trailing JSON value")
		}
		return value, fmt.Errorf("decode input: %w", err)
	}
	return value, nil
}

func validateRequestID(requestID string) error {
	if !IsValidRequestID(requestID) {
		return errors.New("invalid request ID")
	}
	return nil
}

// IsValidRequestID reports whether a request ID is safe for contracts and logs.
func IsValidRequestID(requestID string) bool {
	return len(requestID) <= maximumRequestIDLen && requestIDPattern.MatchString(requestID)
}
