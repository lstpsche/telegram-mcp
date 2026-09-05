package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/session"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
)

func TestKeychainSessionStorageMapsMissingAndDeletes(t *testing.T) {
	t.Parallel()

	secretStore := &memorySecretStore{}
	storage, err := NewKeychainSessionStorage(secretStore, TestEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.LoadSession(context.Background()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("LoadSession() error = %v, want session.ErrNotFound", err)
	}
	if exists, err := storage.Exists(context.Background()); err != nil || exists {
		t.Fatalf("Exists() before StoreSession() = %t, %v", exists, err)
	}
	input := []byte("session material")
	if err := storage.StoreSession(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	clear(input)
	loaded, err := storage.LoadSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded) != "session material" {
		t.Fatalf("LoadSession() = %q", loaded)
	}
	clear(loaded)
	if exists, err := storage.Exists(context.Background()); err != nil || !exists {
		t.Fatalf("Exists() after StoreSession() = %t, %v", exists, err)
	}
	if err := storage.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.LoadSession(context.Background()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("LoadSession() after Delete() error = %v", err)
	}
}

func TestKeychainSessionStorageClearsDataReturnedWithError(t *testing.T) {
	t.Parallel()

	value := []byte("partial session material")
	storage, err := NewKeychainSessionStorage(&errorSecretStore{value: value}, TestEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := storage.LoadSession(context.Background()); err == nil || loaded != nil {
		t.Fatalf("LoadSession() = %q, %v", loaded, err)
	}
	for _, character := range value {
		if character != 0 {
			t.Fatal("LoadSession() did not clear partial data returned with an error")
		}
	}
}

type memorySecretStore struct {
	value []byte
}

type errorSecretStore struct {
	value []byte
}

func (*errorSecretStore) Put(context.Context, string, []byte) error { return nil }

func (s *errorSecretStore) Get(context.Context, string) ([]byte, error) {
	return s.value, errors.New("read failed")
}

func (*errorSecretStore) Delete(context.Context, string) error { return nil }

func (m *memorySecretStore) Put(_ context.Context, _ string, value []byte) error {
	m.value = append(m.value[:0], value...)
	return nil
}

func (m *memorySecretStore) Get(context.Context, string) ([]byte, error) {
	if len(m.value) == 0 {
		return nil, keychain.ErrNotFound
	}
	return append([]byte(nil), m.value...), nil
}

func (m *memorySecretStore) Delete(context.Context, string) error {
	clear(m.value)
	m.value = nil
	return nil
}
