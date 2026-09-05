// Package keychaincheck exercises synthetic items through the same native store
// as the account runtime, without accessing account credentials or metadata.
package keychaincheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
)

const probeService = "dev.telegram-mcp.keychain.qualification"

var tokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var (
	errMismatch = errors.New("synthetic value differs")
	errExists   = errors.New("synthetic item already exists")
	errPresent  = errors.New("synthetic item remains present")
)

type Result struct {
	OK           bool   `json:"ok"`
	Category     string `json:"category"`
	NativeStatus int32  `json:"native_status,omitempty"`
}

type secretStore interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

// Run accepts only fixed actions and a random metadata token. The namespace and
// non-sensitive fixture bytes cannot be supplied by the caller.
func Run(ctx context.Context, args []string, output io.Writer) int {
	return run(ctx, args, output, func() (secretStore, error) { return keychain.New(probeService) })
}

func run(ctx context.Context, args []string, output io.Writer, open func() (secretStore, error)) int {
	result := Result{Category: "invalid_input"}
	if len(args) == 2 && tokenPattern.MatchString(args[1]) && validAction(args[0]) {
		err := ctx.Err()
		if err == nil {
			var store secretStore
			store, err = open()
			if err == nil {
				err = execute(ctx, store, args[0], "qualification-"+args[1])
			}
		}
		result = classify(err)
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return 1
	}
	if !result.OK {
		return 1
	}
	return 0
}

func validAction(action string) bool {
	switch action {
	case "create", "read", "update", "verify", "delete", "absent":
		return true
	default:
		return false
	}
}

func execute(ctx context.Context, store secretStore, action, account string) error {
	initial := sha256.Sum256([]byte("telegram-mcp/initial/" + account))
	replacement := sha256.Sum256([]byte("telegram-mcp/replacement/" + account))
	defer clear(initial[:])
	defer clear(replacement[:])
	switch action {
	case "create":
		existing, err := store.Get(ctx, account)
		clear(existing)
		if err == nil {
			return errExists
		}
		if !errors.Is(err, keychain.ErrNotFound) {
			return err
		}
		if err := store.Put(ctx, account, initial[:]); err != nil {
			return err
		}
		return compare(ctx, store, account, initial[:])
	case "read":
		return compare(ctx, store, account, initial[:])
	case "update":
		if err := compare(ctx, store, account, initial[:]); err != nil {
			return err
		}
		if err := store.Put(ctx, account, replacement[:]); err != nil {
			return err
		}
		return compare(ctx, store, account, replacement[:])
	case "verify":
		return compare(ctx, store, account, replacement[:])
	case "delete":
		return store.Delete(ctx, account)
	case "absent":
		value, err := store.Get(ctx, account)
		clear(value)
		if errors.Is(err, keychain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return errPresent
	}
	return errors.New("invalid synthetic action")
}

func compare(ctx context.Context, store secretStore, account string, expected []byte) error {
	value, err := store.Get(ctx, account)
	defer clear(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(value, expected) {
		return errMismatch
	}
	return nil
}

func classify(err error) Result {
	if err == nil {
		return Result{OK: true, Category: "ok"}
	}
	result := Result{Category: "native_failure"}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		result.Category = "cancelled"
	case errors.Is(err, errExists):
		result.Category = "already_exists"
	case errors.Is(err, errMismatch):
		result.Category = "value_mismatch"
	case errors.Is(err, errPresent):
		result.Category = "still_present"
	case errors.Is(err, keychain.ErrNotFound):
		result.Category = "not_found"
	case errors.Is(err, keychain.ErrKeychainLocked):
		result.Category = "interaction_unavailable"
	case errors.Is(err, keychain.ErrWrongKeychain):
		result.Category = "wrong_keychain"
	case errors.Is(err, keychain.ErrUnsupported):
		result.Category = "unsupported"
	}
	var native *keychain.StatusError
	if errors.As(err, &native) {
		result.NativeStatus = native.Status
	}
	return result
}
