package telegram

import (
	"context"
	"errors"
	"sync"

	"github.com/gotd/td/session"
	"github.com/lstpsche/telegram-mcp/internal/secrets"
)

const (
	KeychainServiceName            = "dev.telegram-mcp.gateway"
	SessionSecretAccount           = "default.session"
	ProductionSessionSecretAccount = "production.session"
	CredentialsSecretAccount       = "default.credentials"
)

type SecretStore interface {
	Put(ctx context.Context, account string, secret []byte) error
	Get(ctx context.Context, account string) ([]byte, error)
	Delete(ctx context.Context, account string) error
}

// SessionStorage adapts the local secret store to gotd's session
// contract. It serializes replacement writes for one account.
type SessionStorage struct {
	store   SecretStore
	account string
	mutex   sync.Mutex
}

func NewSessionStorage(store SecretStore, environment string) (*SessionStorage, error) {
	if store == nil {
		return nil, errors.New("session secret store is required")
	}
	if environment != TestEnvironment && environment != ProductionEnvironment {
		return nil, ErrInvalidConfig
	}
	account := SessionSecretAccount
	if environment == ProductionEnvironment {
		account = ProductionSessionSecretAccount
	}
	return &SessionStorage{store: store, account: account}, nil
}

func (s *SessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session storage is not initialized")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	value, err := s.store.Get(ctx, s.account)
	if errors.Is(err, secrets.ErrNotFound) {
		clear(value)
		return nil, session.ErrNotFound
	}
	if err != nil {
		clear(value)
		return nil, err
	}
	return value, nil
}

// Exists reports whether the secret store contains a gotd session without retaining
// the loaded session bytes.
func (s *SessionStorage) Exists(ctx context.Context) (bool, error) {
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

func (s *SessionStorage) StoreSession(ctx context.Context, data []byte) error {
	if s == nil || s.store == nil {
		return errors.New("session storage is not initialized")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.store.Put(ctx, s.account, data)
}

func (s *SessionStorage) Delete(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("session storage is not initialized")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.store.Delete(ctx, s.account)
}

var _ session.Storage = (*SessionStorage)(nil)
