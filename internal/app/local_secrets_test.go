package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/secrets"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestLocalCredentialsSurviveApplicationRestart(t *testing.T) {
	ctx := context.Background()
	a, _ := newTestApplication(t)
	local, err := secrets.NewFileStore(a.paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	a.secrets = local
	if err := a.Configure(ctx, tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	storage, err := tgaccount.NewSessionStorage(local, tgaccount.TestEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.StoreSession(ctx, []byte("synthetic session")); err != nil {
		t.Fatal(err)
	}
	restartedStore, err := secrets.NewFileStore(a.paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(a.paths, restartedStore)
	if err != nil {
		t.Fatal(err)
	}
	constructed := false
	restarted.factory = func(config tgaccount.Config, session *tgaccount.SessionStorage, _ tgaccount.Mode) (accountRuntime, error) {
		constructed = true
		if config.APIID != 12345 || config.Environment != tgaccount.TestEnvironment || config.TestDC != 2 {
			t.Fatal("credential tuple changed")
		}
		value, err := session.LoadSession(ctx)
		defer clear(value)
		if err != nil || string(value) != "synthetic session" {
			t.Fatal("session was not reused", err)
		}
		return &fakeRuntime{}, nil
	}
	db, repo, err := restarted.openRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := restarted.account(ctx, repo, tgaccount.ModeNoUpdates); err != nil {
		t.Fatal(err)
	}
	if !constructed {
		t.Fatal("runtime was not constructed")
	}
}

func TestConfiguredAccountRejectsLostLocalStoreBeforePromptOrClient(t *testing.T) {
	ctx := context.Background()
	a, _ := newTestApplication(t)
	local, err := secrets.NewFileStore(a.paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	a.secrets = local
	if err := a.Configure(ctx, tgaccount.TestEnvironment, 2, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a.paths.StateDir, "secrets.json")); err != nil {
		t.Fatal(err)
	}
	if err := a.Configure(ctx, tgaccount.TestEnvironment, 2, func(context.Context) (int, []byte, error) {
		t.Fatal("prompt opened after secret loss")
		return 0, nil, nil
	}); !errors.Is(err, ErrLocalSecretsUnavailable) {
		t.Fatal(err)
	}
	a.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		t.Fatal("client constructed after secret loss")
		return nil, nil
	}
	db, repo, err := a.openRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := a.account(ctx, repo, tgaccount.ModeNoUpdates); !errors.Is(err, ErrLocalSecretsUnavailable) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(a.paths.StateDir, "secrets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lost store recreated", err)
	}
}
