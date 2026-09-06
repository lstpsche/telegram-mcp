package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lstpsche/telegram-mcp/internal/secrets"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
	"github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

var ErrMigrationUnavailable = errors.New("legacy Keychain migration requires a native macOS migration build")

// MigrateKeychain is an explicit one-time copy. Normal account operations never
// consult Keychain or substitute another store when the local file fails.
func (a *Application) MigrateKeychain(ctx context.Context) error {
	legacy, err := keychain.New(tgaccount.KeychainServiceName)
	if err != nil {
		return err
	}
	err = a.migrateSecrets(ctx, legacy)
	if errors.Is(err, keychain.ErrUnsupported) {
		return errors.Join(ErrMigrationUnavailable, err)
	}
	return err
}

func (a *Application) migrateSecrets(ctx context.Context, legacy tgaccount.SecretStore) error {
	destination, ok := a.secrets.(*secrets.FileStore)
	if !ok {
		return secrets.ErrInvalidStore
	}
	return a.withMaintenance(ctx, func(ctx context.Context, _ *sql.DB, repository *store.Repository) error {
		config, configured, err := repository.Config(ctx)
		if err != nil {
			return err
		}
		if !configured {
			return ErrConfigurationRequired
		}
		values := make(map[string][]byte, 4)
		defer func() {
			for _, value := range values {
				clear(value)
			}
		}()
		for _, name := range []string{tgaccount.CredentialsSecretAccount, tgaccount.SessionSecretAccount, tgaccount.ProductionSessionSecretAccount, cursorSecretAccount} {
			value, err := legacy.Get(ctx, name)
			if errors.Is(err, secrets.ErrNotFound) {
				clear(value)
				continue
			}
			if err != nil {
				clear(value)
				return err
			}
			values[name] = value
		}
		credentials, err := tgaccount.LoadCredentials(ctx, migrationSnapshot(values))
		defer clear(credentials.APIHash)
		if err != nil {
			return err
		}
		if credentials.APIID != config.APIID || credentials.Environment != config.Environment || credentials.TestDC != config.TestDC {
			return ErrConfigurationRequired
		}
		_, authorized, err := repository.Authorization(ctx)
		if err != nil {
			return err
		}
		sessionName := tgaccount.SessionSecretAccount
		if config.Environment == tgaccount.ProductionEnvironment {
			sessionName = tgaccount.ProductionSessionSecretAccount
		}
		if authorized && len(values[sessionName]) == 0 {
			return ErrLocalSecretsUnavailable
		}
		if len(values[cursorSecretAccount]) != 0 && len(values[cursorSecretAccount]) != 32 {
			return secrets.ErrInvalidStore
		}
		return destination.Import(ctx, values)
	})
}

// The immutable migration snapshot prevents a second Keychain read from mixing
// credentials from different observations before atomic publication.
type migrationSnapshot map[string][]byte

func (s migrationSnapshot) Get(_ context.Context, name string) ([]byte, error) {
	value, ok := s[name]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}
func (migrationSnapshot) Put(context.Context, string, []byte) error { return secrets.ErrInvalidStore }
func (migrationSnapshot) Delete(context.Context, string) error      { return secrets.ErrInvalidStore }
