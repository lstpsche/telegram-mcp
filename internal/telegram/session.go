package telegram

import (
	"context"
	"errors"
	"sync"

	"github.com/gotd/td/session"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
)

const (
	KeychainServiceName  = "dev.tgcontext.gateway"
	SessionSecretAccount = "default.session"
	APIHashSecretAccount = "default.api-hash"
)

type SecretStore interface {
	Put(ctx context.Context, account string, secret []byte) error
	Get(ctx context.Context, account string) ([]byte, error)
	Delete(ctx context.Context, account string) error
}

// KeychainSessionStorage adapts the native secret store to gotd's session
// contract. It serializes replacement writes for one account.
type KeychainSessionStorage struct {
	store SecretStore
	mutex sync.Mutex
}

func NewKeychainSessionStorage(store SecretStore) (*KeychainSessionStorage, error) {
	if store == nil {
		return nil, errors.New("session secret store is required")
	}
	return &KeychainSessionStorage{store: store}, nil
}

func (s *KeychainSessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session storage is not initialized")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	value, err := s.store.Get(ctx, SessionSecretAccount)
	if errors.Is(err, keychain.ErrNotFound) {
		clear(value)
		return nil, session.ErrNotFound
	}
	if err != nil {
		clear(value)
		return nil, err
	}
	return value, nil
}

// Exists reports whether Keychain contains a gotd session without retaining
// the loaded session bytes.
func (s *KeychainSessionStorage) Exists(ctx context.Context) (bool, error) {
	value, err := s.LoadSession(ctx)
	clear(value)
	if errors.Is(err, session.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *KeychainSessionStorage) StoreSession(ctx context.Context, data []byte) error {
	if s == nil || s.store == nil {
		return errors.New("session storage is not initialized")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.store.Put(ctx, SessionSecretAccount, data)
}

func (s *KeychainSessionStorage) Delete(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("session storage is not initialized")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.store.Delete(ctx, SessionSecretAccount)
}

var _ session.Storage = (*KeychainSessionStorage)(nil)
