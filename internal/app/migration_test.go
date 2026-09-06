package app

import (
	"context"
	"errors"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/secrets"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestLegacyMigrationPublishesCompleteStoreAndRetainsSource(t *testing.T) {
	ctx := context.Background()
	a := authorizedTextApplication(t, &fakeRuntime{})
	legacy := a.secrets
	if err := legacy.Put(ctx, tgaccount.SessionSecretAccount, []byte("synthetic session")); err != nil {
		t.Fatal(err)
	}
	key, err := a.cursorKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clear(key)
	local, err := secrets.NewFileStore(a.paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	a.secrets = local
	if err := a.migrateSecrets(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{tgaccount.CredentialsSecretAccount, tgaccount.SessionSecretAccount, cursorSecretAccount} {
		value, err := local.Get(ctx, name)
		if err != nil || len(value) == 0 {
			t.Fatal("migration omitted item", err)
		}
		clear(value)
		original, err := legacy.Get(ctx, name)
		if err != nil || len(original) == 0 {
			t.Fatal("migration deleted legacy item", err)
		}
		clear(original)
	}
	if err := a.migrateSecrets(ctx, legacy); !errors.Is(err, secrets.ErrStoreExists) {
		t.Fatal("migration overwrote local store", err)
	}
}
func TestLegacyMigrationRejectsMissingSessionAndWrongCredentials(t *testing.T) {
	ctx := context.Background()
	a := authorizedTextApplication(t, &fakeRuntime{})
	legacy := a.secrets
	local, err := secrets.NewFileStore(a.paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	a.secrets = local
	if err := a.migrateSecrets(ctx, legacy); !errors.Is(err, ErrLocalSecretsUnavailable) {
		t.Fatal("authorized session absence ignored", err)
	}
	if _, err := local.Get(ctx, tgaccount.CredentialsSecretAccount); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("partial migration published", err)
	}
	if err := tgaccount.StoreCredentials(ctx, legacy, tgaccount.Config{Environment: tgaccount.TestEnvironment, APIID: 999, APIHash: []byte("0123456789abcdef0123456789abcdef"), TestDC: 2}); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateSecrets(ctx, legacy); !errors.Is(err, ErrConfigurationRequired) {
		t.Fatal("mixed credential tuple accepted", err)
	}
}
