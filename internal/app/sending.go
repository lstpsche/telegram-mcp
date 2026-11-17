package app

import (
	"context"
	"errors"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

var ErrSendStatusUnavailable = errors.New("retained send status is unavailable")

// FullSend inspects the current account's explicit account-wide send access.
func (a *Application) FullSend(ctx context.Context) (enabled bool, err error) {
	err = a.withPolicy(ctx, func(lease *policy.Lease) error {
		enabled, err = lease.FullSend(ctx)
		return err
	})
	return enabled, err
}

func (a *Application) SetFullSend(ctx context.Context, enabled bool) error {
	return a.withPolicy(ctx, func(lease *policy.Lease) error { return lease.SetFullSend(ctx, enabled) })
}

// SendOperationStatus inspects retained local facts without requiring current
// authorization, send access, credentials, a running daemon, or Telegram I/O.
func (a *Application) SendOperationStatus(ctx context.Context, operationID string) (operation model.SendOperation, resultError error) {
	if _, err := model.ParseOperationID(operationID); err != nil {
		return model.SendOperation{}, err
	}
	if a == nil {
		return model.SendOperation{}, ErrSendStatusUnavailable
	}
	database, err := store.OpenReadOnly(ctx, a.paths.Database)
	if err != nil {
		return model.SendOperation{}, errors.Join(ErrSendStatusUnavailable, err)
	}
	defer func() { resultError = errors.Join(resultError, database.Close()) }()
	repository, err := store.NewOutgoingRepository(database)
	if err != nil {
		return model.SendOperation{}, errors.Join(ErrSendStatusUnavailable, err)
	}
	retained, exists, err := repository.Find(ctx, operationID, a.now())
	if err != nil {
		return model.SendOperation{}, errors.Join(ErrSendStatusUnavailable, err)
	}
	if !exists {
		return model.SendOperation{}, ErrSendStatusUnavailable
	}
	operation = retained.Public()
	if err := operation.Validate(); err != nil {
		return model.SendOperation{}, errors.Join(ErrSendStatusUnavailable, err)
	}
	return operation, nil
}
