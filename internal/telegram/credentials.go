package telegram

import (
	"context"
	"encoding/json"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

// Application credentials form one atomic Keychain value. The adapter never
// combines a hash from one configuration with an API ID/DC from another.
type credentials struct {
	Version int    `json:"version"`
	APIID   int    `json:"api_id"`
	APIHash []byte `json:"api_hash"`
	TestDC  int    `json:"test_dc"`
}

func StoreCredentials(ctx context.Context, store SecretStore, config Config) error {
	if err := ValidateConfig(config); err != nil {
		return err
	}
	if store == nil {
		return ErrInvalidConfig
	}
	value, err := json.Marshal(credentials{Version: 1, APIID: config.APIID, APIHash: config.APIHash, TestDC: config.TestDC})
	if err != nil {
		return ErrInvalidConfig
	}
	defer clear(value)
	return store.Put(ctx, CredentialsSecretAccount, value)
}

func LoadCredentials(ctx context.Context, store SecretStore) (Config, error) {
	if store == nil {
		return Config{}, ErrInvalidConfig
	}
	value, err := store.Get(ctx, CredentialsSecretAccount)
	defer clear(value)
	if err != nil {
		return Config{}, err
	}
	stored, err := model.DecodeStrict[credentials](value)
	config := Config{APIID: stored.APIID, APIHash: stored.APIHash, TestDC: stored.TestDC}
	if err != nil || stored.Version != 1 || ValidateConfig(config) != nil {
		clear(config.APIHash)
		return Config{}, ErrInvalidConfig
	}
	return config, nil
}
