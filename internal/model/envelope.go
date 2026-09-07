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
	Kind              ReadEffectKind `json:"kind"`
	ThroughMessageID  *MessageID     `json:"through_message_id,omitempty"`
	ThroughMessageIDs []MessageID    `json:"through_message_ids,omitempty"`
}

func NoReadEffect() ReadEffect { return ReadEffect{Kind: ReadEffectNone} }

func (e ReadEffect) Validate() error {
	switch e.Kind {
	case ReadEffectNone:
		if e.ThroughMessageID != nil || len(e.ThroughMessageIDs) > 0 {
			return errors.New("none read effect cannot have a message reference")
		}
	case ReadEffectHistoryMarkedRead, ReadEffectContentMarkedRead:
		if len(e.ThroughMessageIDs) > 0 {
			if e.Kind != ReadEffectHistoryMarkedRead || e.ThroughMessageID != nil || len(e.ThroughMessageIDs) > MaximumContextTargets {
				return errors.New("invalid batch read effect")
			}
			seen := map[PeerID]bool{}
			for _, id := range e.ThroughMessageIDs {
				if !id.valid() || seen[id.Peer()] {
					return errors.New("invalid batch read boundary")
				}
				seen[id.Peer()] = true
			}
		} else if e.ThroughMessageID == nil || !e.ThroughMessageID.valid() {
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

// ScopeCoverage describes selection exclusions separately from bounded traversal.
type ScopeCoverage struct {
	CatchUp        *CatchUpCoverage `json:"catch_up,omitempty"`
	ID             ScopeID          `json:"id"`
	TotalPeers     int              `json:"total_peers"`
	EligiblePeers  int              `json:"eligible_peers"`
	ExcludedPeers  int              `json:"excluded_peers"`
	QueriedPeers   int              `json:"queried_peers"`
	CompletedPeers int              `json:"completed_peers"`
}

func (c ScopeCoverage) Validate() error {
	if _, err := ParseScopeID(c.ID.String()); err != nil {
		return err
	}
	if c.TotalPeers < 0 || c.TotalPeers > 20 || c.EligiblePeers < 0 || c.ExcludedPeers < 0 || c.EligiblePeers+c.ExcludedPeers != c.TotalPeers || c.QueriedPeers < 0 || c.QueriedPeers > c.EligiblePeers || c.CompletedPeers < 0 || c.CompletedPeers > c.EligiblePeers {
		return errors.New("invalid scope coverage")
	}
	if c.CatchUp != nil {
		if token := c.CatchUp.Checkpoint; token != "" && (c.CompletedPeers != c.EligiblePeers || len(token) > maximumCursorLength || !strings.HasPrefix(token, "cu1.") || !cursorPattern.MatchString(token)) {
			return errors.New("invalid catch-up checkpoint coverage")
		}
		if _, err := ParseDateWindow(c.CatchUp.Since, c.CatchUp.Until); err != nil || c.CatchUp.Peers == nil || len(c.CatchUp.Peers) != c.EligiblePeers {
			return errors.New("invalid catch-up coverage")
		}
		fetched, completed := 0, 0
		var previous PeerID
		for _, peer := range c.CatchUp.Peers {
			if _, err := ParsePeerID(peer.Peer.String()); err != nil || peer.Peer.String() <= previous.String() || peer.Fetched < 0 || peer.Returned < 0 || peer.Returned > peer.Fetched {
				return errors.New("invalid catch-up peer coverage")
			}
			switch peer.State {
			case "complete":
				completed++
			case "pending", "in_progress":
			default:
				return errors.New("invalid catch-up peer state")
			}
			previous = peer.Peer
			fetched += peer.Fetched
		}
		if fetched > MaximumPageSize || completed != c.CompletedPeers {
			return errors.New("inconsistent catch-up coverage")
		}
	}
	return nil
}

// Envelope is the shared v1 output shape for successful MCP results.
type Envelope[T any] struct {
	Searches         []SearchAssociation `json:"searches,omitempty"`
	Contexts         []MessageContext    `json:"contexts,omitempty"`
	SchemaVersion    string              `json:"schema_version"`
	Scope            *ScopeCoverage      `json:"scope,omitempty"`
	RequestID        string              `json:"request_id"`
	Freshness        Freshness           `json:"freshness"`
	Partial          bool                `json:"partial"`
	ReadEffect       ReadEffect          `json:"read_effect"`
	Items            []T                 `json:"items"`
	NextCursor       *string             `json:"next_cursor"`
	Warnings         []WarningCode       `json:"warnings"`
	UntrustedContent bool                `json:"untrusted_content"`
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
	if len(e.Searches) > 0 {
		if len(e.Searches) > MaximumSearchRequests || len(e.Contexts) > 0 || e.NextCursor != nil || e.Scope != nil || e.ReadEffect.Kind != NoReadEffect().Kind {
			return errors.New("invalid batch search envelope")
		}
		for _, search := range e.Searches {
			if search.Messages == nil || len(search.Messages) > MaximumPageSize {
				return errors.New("invalid search association")
			}
			seen := map[MessageID]bool{}
			for _, id := range search.Messages {
				if !id.valid() || seen[id] {
					return errors.New("invalid search reference")
				}
				seen[id] = true
			}
			if search.NextCursor != nil && (len(*search.NextCursor) > 4096 || !cursorPattern.MatchString(*search.NextCursor)) {
				return errors.New("invalid search cursor")
			}
			if search.Scope != nil {
				if err := search.Scope.Validate(); err != nil {
					return err
				}
				if search.Scope.CatchUp != nil {
					return errors.New("invalid search coverage")
				}
			}
		}
	}

	if len(e.Contexts) > 0 {
		if len(e.Contexts) > MaximumContextTargets || len(e.ReadEffect.ThroughMessageIDs) == 0 {
			return errors.New("invalid batch context envelope")
		}
		targets := map[MessageID]bool{}
		for _, c := range e.Contexts {
			if !c.Target.valid() || targets[c.Target] || len(c.Messages) == 0 || len(c.Messages) > MaximumPageSize {
				return errors.New("invalid context association")
			}
			targets[c.Target] = true
			found := false
			seen := map[MessageID]bool{}
			for _, id := range c.Messages {
				if !id.valid() || id.Peer() != c.Target.Peer() || seen[id] {
					return errors.New("invalid context message reference")
				}
				seen[id] = true
				found = found || id == c.Target
			}
			if !found {
				return errors.New("context target missing from association")
			}
		}
	}
	if e.Scope != nil {
		if e.Scope.CatchUp != nil && e.Scope.CatchUp.Checkpoint != "" && e.NextCursor != nil {
			return errors.New("unfinished catch-up cannot issue a checkpoint")
		}
		if err := e.Scope.Validate(); err != nil {
			return err
		}
	}
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
	ErrorReadEffectUncertain ErrorCategory = "read_effect_uncertain"
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
	case ErrorReadEffectUncertain:
		return "No content was released; the Telegram read effect may have occurred", true
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
