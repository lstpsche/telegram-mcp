package telegram

import "errors"

// Synchronization errors identify safe diagnostic categories. They do not carry
// account identifiers, remote responses or checkpoint values. Wrapped causes
// remain private and must never be logged or serialized.
var (
	ErrUpdateGap             = errors.New("Telegram synchronization requires gap recovery")
	ErrUpdateRecoveryLimit   = errors.New("Telegram synchronization recovery limit was reached")
	ErrUpdateRecoveryStalled = errors.New("Telegram synchronization recovery made no progress")
	ErrUpdateResponseLimit   = errors.New("Telegram synchronization response exceeded its size limit")
	ErrUpdateStateInvalid    = errors.New("Telegram synchronization state is invalid")
)
