// Package keychain stores Telegram MCP secrets in the current user's unlocked
// macOS login keychain through Security.framework.
package keychain

import (
	"context"
	"errors"
	"fmt"
	"github.com/lstpsche/telegram-mcp/internal/secrets"
	"regexp"
)

const MaximumSecretBytes = 1024 * 1024

var (
	ErrNotFound       = secrets.ErrNotFound
	ErrKeychainLocked = errors.New("login keychain is locked or interaction would be required")
	ErrWrongKeychain  = errors.New("default keychain is not the login keychain")
	ErrUnsupported    = errors.New("native macOS keychain is unavailable")
)

var (
	servicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	accountPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type Store struct {
	service string
}

func New(service string) (*Store, error) {
	if !servicePattern.MatchString(service) {
		return nil, errors.New("keychain service must be a bounded metadata token")
	}
	return &Store{service: service}, nil
}

func (s *Store) Put(ctx context.Context, account string, secret []byte) error {
	if err := validateCall(ctx, s, account); err != nil {
		return err
	}
	if len(secret) == 0 || len(secret) > MaximumSecretBytes {
		return errors.New("secret length is outside the allowed range")
	}
	return s.put(account, secret)
}

// Get returns a fresh byte slice. The caller must clear it immediately after
// use and keep any unavoidable immutable conversion at the narrow downstream
// API boundary.
func (s *Store) Get(ctx context.Context, account string) ([]byte, error) {
	if err := validateCall(ctx, s, account); err != nil {
		return nil, err
	}
	secret, err := s.get(account)
	if err != nil {
		clear(secret)
		return nil, err
	}
	if len(secret) == 0 || len(secret) > MaximumSecretBytes {
		clear(secret)
		return nil, errors.New("stored secret length is outside the allowed range")
	}
	return secret, nil
}

func (s *Store) Delete(ctx context.Context, account string) error {
	if err := validateCall(ctx, s, account); err != nil {
		return err
	}
	err := s.delete(account)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

type StatusError struct {
	Operation string
	Status    int32
	kind      error
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("keychain %s failed with status %d", e.Operation, e.Status)
}

func (e *StatusError) Unwrap() error { return e.kind }

func validateAccount(account string) error {
	if !accountPattern.MatchString(account) {
		return errors.New("keychain account must be a bounded metadata token")
	}
	return nil
}

func validateCall(ctx context.Context, store *Store, account string) error {
	if ctx == nil {
		return errors.New("keychain context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || !servicePattern.MatchString(store.service) {
		return errors.New("keychain store is not initialized")
	}
	return validateAccount(account)
}
