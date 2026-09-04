package model

import (
	"context"
	"errors"
	"fmt"
)

// MaximumRPCIDBytes bounds the normalized JSON encoding of a reflected ID.
const MaximumRPCIDBytes = 1024

// Chat and Message contain untrusted display data. They are never audit records.
type Chat struct {
	ID    PeerID `json:"id"`
	Title string `json:"title"`
}

type Message struct {
	ID     MessageID `json:"id"`
	Author PeerID    `json:"author"`
	Date   string    `json:"date"`
	Text   string    `json:"text"`
}

// Candidate carries normalization evidence only inside the application.
// Unsafe source bodies must not be copied into Message.Text.
type Candidate struct {
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
