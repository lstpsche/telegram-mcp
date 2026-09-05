package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
)

func TestCursorKeyPersistsIndependentlyAndSurvivesRestart(t *testing.T) {
	a, secrets := newTestApplication(t)
	key, err := a.cursorKey(context.Background())
	if err != nil || len(key) != 32 {
		t.Fatal("key initialization failed", err)
	}
	defer clear(key)
	second, err := New(a.paths, secrets)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := second.cursorKey(context.Background())
	if err != nil || !bytes.Equal(key, reloaded) {
		t.Fatal("restart changed cursor key")
	}
	clear(reloaded)
	secrets.set(cursorSecretAccount, []byte("corrupt"))
	if value, err := a.cursorKey(context.Background()); err == nil || value != nil {
		t.Fatal("corrupt key replaced")
	}
	preserved, err := secrets.Get(context.Background(), cursorSecretAccount)
	if err != nil || string(preserved) != "corrupt" {
		t.Fatal("corrupt value was overwritten")
	}
}

type failedCursorSecrets struct {
	getErr, putErr error
	puts           int
}

func (s *failedCursorSecrets) Get(context.Context, string) ([]byte, error) { return nil, s.getErr }
func (s *failedCursorSecrets) Put(context.Context, string, []byte) error   { s.puts++; return s.putErr }
func (s *failedCursorSecrets) Delete(context.Context, string) error        { return nil }

func TestCursorKeyFailureDoesNotSubstituteOrWrite(t *testing.T) {
	a, _ := newTestApplication(t)
	failure := errors.New("synthetic failure")
	secrets := &failedCursorSecrets{getErr: failure}
	a.secrets = secrets
	if key, err := a.cursorKey(context.Background()); !errors.Is(err, failure) || key != nil || secrets.puts != 0 {
		t.Fatal("read failure replaced key")
	}
	secrets.getErr = keychain.ErrNotFound
	secrets.putErr = failure
	if key, err := a.cursorKey(context.Background()); !errors.Is(err, failure) || key != nil {
		t.Fatal("write failure released key")
	}
	secrets.puts = 0
	a.random = bytes.NewReader(nil)
	if key, err := a.cursorKey(context.Background()); err == nil || key != nil || secrets.puts != 0 {
		t.Fatal("entropy failure wrote key")
	}
}
