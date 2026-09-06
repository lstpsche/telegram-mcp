package app

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/session"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
	metastore "github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestProductionAccountSessionIsolationRestartAndLogout(t *testing.T) {
	ctx := context.Background()
	application, secrets := newTestApplication(t)
	if err := application.Configure(ctx, tgaccount.ProductionEnvironment, 0, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{authResult: tgaccount.AuthResult{Performed: true}}
	application.factory = func(config tgaccount.Config, storage *tgaccount.SessionStorage, mode tgaccount.Mode) (accountRuntime, error) {
		if config.Environment != tgaccount.ProductionEnvironment || config.TestDC != 0 {
			t.Fatal("wrong account environment")
		}
		if _, err := storage.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
			t.Fatal("unexpected initial production session", err)
		}
		if err := storage.StoreSession(ctx, []byte("synthetic production session")); err != nil {
			t.Fatal(err)
		}
		return runtime, nil
	}
	if _, err := application.Authenticate(ctx, tgaccount.AuthMethodPhone, fakePrompt{}); err != nil {
		t.Fatal(err)
	}
	first := authorizationState(t, application.paths.Database)
	restarted, err := New(application.paths, secrets)
	if err != nil {
		t.Fatal(err)
	}
	secrets.set(tgaccount.SessionSecretAccount, []byte("synthetic test session"))
	restarted.factory = func(config tgaccount.Config, storage *tgaccount.SessionStorage, _ tgaccount.Mode) (accountRuntime, error) {
		value, err := storage.LoadSession(ctx)
		defer clear(value)
		if err != nil || string(value) != "synthetic production session" || config.Environment != tgaccount.ProductionEnvironment {
			t.Fatal("production loaded wrong session", err)
		}
		return &fakeRuntime{}, nil
	}
	if outcome, err := restarted.Authenticate(ctx, tgaccount.AuthMethodQR, fakePrompt{}); err != nil || outcome.Performed {
		t.Fatal("restart did not reuse authorization", err)
	}
	if authorizationState(t, application.paths.Database).Epoch != first.Epoch {
		t.Fatal("restart rotated epoch")
	}
	if err := restarted.Configure(ctx, tgaccount.TestEnvironment, 2, staticConfiguration(12345)); !errors.Is(err, metastore.ErrAuthorizationExists) {
		t.Fatal("live environment switched", err)
	}
	if err := restarted.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := secrets.value(tgaccount.ProductionSessionSecretAccount); ok {
		t.Fatal("production session survived logout")
	}
	if value, ok := secrets.value(tgaccount.SessionSecretAccount); !ok || string(value) != "synthetic test session" {
		t.Fatal("logout changed test session")
	}
	status, err := restarted.Status(ctx)
	if err != nil || status.Authorized || status.Environment != tgaccount.ProductionEnvironment || status.TestDC != 0 || !status.PhoneCheckPassed || status.QRCheckPassed {
		t.Fatal("incorrect production status", err)
	}
}

func TestEitherSessionBlocksConfigurationBeforePrompting(t *testing.T) {
	for _, key := range []string{tgaccount.SessionSecretAccount, tgaccount.ProductionSessionSecretAccount} {
		t.Run(key, func(t *testing.T) {
			application, secrets := newTestApplication(t)
			secrets.set(key, []byte("synthetic surviving session"))
			prompted := false
			err := application.Configure(context.Background(), tgaccount.ProductionEnvironment, 0, func(context.Context) (int, []byte, error) { prompted = true; return 0, nil, nil })
			if !errors.Is(err, metastore.ErrAuthorizationExists) || prompted {
				t.Fatal("surviving session did not block configuration", err)
			}
			if _, exists := secrets.value(tgaccount.CredentialsSecretAccount); exists {
				t.Fatal("credentials changed")
			}
		})
	}
}

func TestMixedEnvironmentCredentialsFailBeforeRuntimeConstruction(t *testing.T) {
	ctx := context.Background()
	application, secrets := newTestApplication(t)
	if err := application.Configure(ctx, tgaccount.ProductionEnvironment, 0, staticConfiguration(12345)); err != nil {
		t.Fatal(err)
	}
	if err := tgaccount.StoreCredentials(ctx, secrets, tgaccount.Config{Environment: tgaccount.TestEnvironment, TestDC: 2, APIID: 12345, APIHash: []byte(testAPIHash)}); err != nil {
		t.Fatal(err)
	}
	application.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		t.Fatal("mixed environment reached runtime")
		return nil, nil
	}
	if _, err := application.Authenticate(ctx, tgaccount.AuthMethodPhone, fakePrompt{}); !errors.Is(err, ErrConfigurationRequired) {
		t.Fatal("mixed credentials accepted", err)
	}
	if _, err := secrets.Get(ctx, tgaccount.ProductionSessionSecretAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatal("production session created", err)
	}
}
