package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaximumRPCIDBytes bounds the normalized JSON encoding of a reflected ID.
const MaximumRPCIDBytes = 1024

// Chat and Message contain untrusted display data. They are never audit records.
type Chat struct {
	Broadcast bool   `json:"broadcast,omitempty"`
	Forum     bool   `json:"forum,omitempty"`
	ID        PeerID `json:"id"`
	Title     string `json:"title"`
}

type ReplyChain struct {
	Depth int    `json:"depth"`
	State string `json:"state"`
}

type Message struct {
	ReplyTo     *MessageID          `json:"reply_to,omitempty"`
	ReplyChain  *ReplyChain         `json:"reply_chain,omitempty"`
	ChannelPost *ChannelPost        `json:"channel_post,omitempty"`
	Forward     *Forward            `json:"forward,omitempty"`
	Voice       *VoiceDescriptor    `json:"voice_note,omitempty"`
	Document    *DocumentDescriptor `json:"document,omitempty"`
	Image       *ImageDescriptor    `json:"image,omitempty"`
	ID          MessageID           `json:"id"`
	Author      PeerID              `json:"author"`
	Date        string              `json:"date"`
	Text        string              `json:"text"`
}

// Candidate carries normalization evidence only inside the application.
// Unsafe source bodies must not be copied into Message.Text.
type Candidate struct {
	Voice *MediaSource
	// SentAt retains only timestamp evidence for date traversal, including excluded bodies.
	SentAt      int64
	Image       *MediaSource
	Document    *MediaSource
	Message     Message
	Protected   bool
	Ephemeral   bool
	Forwarded   bool
	Quoted      bool
	Unsupported bool
}

// HistoryQuery uses inclusive grant bounds and exclusive Before selection.
// Target selects context; BeforeCount/AfterCount count neighboring messages.
type HistoryQuery struct {
	Peer        PeerID
	Before      int32
	ReplyDepth  int
	Target      int32
	BeforeCount int
	AfterCount  int
	MinID       int32
	MaxID       int32
	Limit       int
}

// OperationError preserves an internal cause while its text is content-free.
// The cause is for errors.Is/As only and must never be logged or serialized.
type OperationError struct {
	Category ErrorCategory
	Cause    error
}

func (e *OperationError) Error() string { return fmt.Sprintf("text operation failed: %s", e.Category) }
func (e *OperationError) Unwrap() error { return e.Cause }

func TextError(category ErrorCategory, cause error) error {
	return &OperationError{Category: category, Cause: cause}
}

func TextErrorCategory(err error) ErrorCategory {
	if err == nil {
		return ""
	}
	var operation *OperationError
	if errors.As(err, &operation) && operation.Category.IsValid() {
		return operation.Category
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorCancelled
	}
	return ErrorInternal
}

// SearchQuery is adapter input. Query text is transient and must never be logged.
type SearchQuery struct {
	Window               *DateWindow
	Peer                 PeerID
	Query                string
	MinID, MaxID, Before int32
	Limit                int
}

type SearchHit struct {
	ReplyTo          *MessageID          `json:"reply_to,omitempty"`
	ChannelPost      *ChannelPost        `json:"channel_post,omitempty"`
	Forward          *Forward            `json:"forward,omitempty"`
	Voice            *VoiceDescriptor    `json:"voice_note,omitempty"`
	Document         *DocumentDescriptor `json:"document,omitempty"`
	Image            *ImageDescriptor    `json:"image,omitempty"`
	ID               MessageID           `json:"id"`
	Author           PeerID              `json:"author"`
	Date             string              `json:"date"`
	Snippet          string              `json:"snippet"`
	SnippetTruncated bool                `json:"snippet_truncated"`
}

// Unread describes the entire granted dialog, not just its authorized body range.
type Unread struct {
	Peer   PeerID `json:"peer"`
	Count  int    `json:"unread_count"`
	Marked bool   `json:"unread_mark"`
}

func NormalizeSearchQuery(query string) (string, error) {
	if !utf8.ValidString(query) || len(query) > 1024 {
		return "", TextError(ErrorInvalidInput, nil)
	}
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 256 {
		return "", TextError(ErrorInvalidInput, nil)
	}
	return query, nil
}

// ValidReply keeps reply navigation inside the exact conversation and older IDs.
func (m Message) ValidReply() bool {
	return m.ReplyTo == nil || (m.ReplyTo.String() != "" && m.ReplyTo.Peer() == m.ID.Peer() && m.ReplyTo.TelegramID() < m.ID.TelegramID())
}
