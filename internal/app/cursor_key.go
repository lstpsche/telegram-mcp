package app

import (
	"context"
	"errors"
	"io"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
)

const cursorSecretAccount = "default.cursor-integrity"

// cursorKey runs under the daemon's account lock, after an epoch is recorded.
// A missing item initializes a distinct key; corrupt/inaccessible items fail.
func (a *Application) cursorKey(ctx context.Context) ([]byte, error) {
	key, err := a.secrets.Get(ctx, cursorSecretAccount)
	if errors.Is(err, keychain.ErrNotFound) {
		clear(key)
		key = make([]byte, 32)
		if _, err := io.ReadFull(a.random, key); err != nil {
			clear(key)
			return nil, model.TextError(model.ErrorInternal, err)
		}
		if err := a.secrets.Put(ctx, cursorSecretAccount, key); err != nil {
			clear(key)
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		clear(key)
		return nil, err
	}
	if len(key) != 32 {
		clear(key)
		return nil, errors.New("cursor integrity key is invalid")
	}
	return key, nil
}
