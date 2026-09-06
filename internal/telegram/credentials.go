package telegram

import (
	"context"
	"encoding/json"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

// Application credentials form one atomic secret-store value. The adapter never
// combines a hash from one configuration with an API ID/environment/DC from another.
type credentials struct {
	Version     int    `json:"version"`
	Environment string `json:"environment,omitempty"`
	APIID       int    `json:"api_id"`
	APIHash     []byte `json:"api_hash"`
	TestDC      int    `json:"test_dc"`
}

func StoreCredentials(ctx context.Context, store SecretStore, config Config) error {
	if err := ValidateConfig(config); err != nil {
		return err
	}
	if store == nil {
		return ErrInvalidConfig
	}
	value, err := json.Marshal(credentials{Version: 2, Environment: config.Environment, APIID: config.APIID, APIHash: config.APIHash, TestDC: config.TestDC})
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
	if err == nil && stored.Version == 1 && stored.Environment == "" {
		// Legacy tuples were issued exclusively for Test DCs.
		stored.Environment = TestEnvironment
	} else if stored.Version != 2 || stored.Environment == "" {
		err = ErrInvalidConfig
	}
	config := Config{Environment: stored.Environment, APIID: stored.APIID, APIHash: stored.APIHash, TestDC: stored.TestDC}
	if err != nil || ValidateConfig(config) != nil {
		clear(config.APIHash)
		return Config{}, ErrInvalidConfig
	}
	return config, nil
}
