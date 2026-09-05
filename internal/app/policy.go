package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

var ErrTextControlUnsupported = errors.New("account runtime does not support text access control")

// Peers performs bounded, human-only discovery while holding exclusive session
// ownership. It never creates a grant or returns message bodies.
func (a *Application) Peers(ctx context.Context) (peers []model.Chat, err error) {
	err = a.withDiscovery(ctx, func(ctx context.Context, discovery humanDiscovery) error {
		var err error
		peers, err = discovery.Discover(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return peers, nil
}

// SavedMessage discovers only the newest Saved Messages reference. It does not
// grant access, return content, or acknowledge history.
func (a *Application) SavedMessage(ctx context.Context) (message model.MessageID, err error) {
	err = a.withDiscovery(ctx, func(ctx context.Context, discovery humanDiscovery) error {
		var err error
		message, err = discovery.DiscoverSavedMessage(ctx)
		return err
	})
	if err != nil {
		return model.MessageID{}, err
	}
	return message, nil
}

type humanDiscovery interface {
	EnableReads(context.Context, *sql.DB, string) error
	Discover(context.Context) ([]model.Chat, error)
	DiscoverSavedMessage(context.Context) (model.MessageID, error)
}

func (a *Application) withDiscovery(ctx context.Context, operation func(context.Context, humanDiscovery) error) (resultError error) {
	if a == nil {
		return errors.New("application is not initialized")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lock, err := daemon.AcquireAccountLock(a.paths.Lock)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, lock.Release()) }()
	database, repository, err := a.openRepository(ctx)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, database.Close()) }()
	authorization, exists, err := repository.Authorization(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return tgaccount.ErrReauthenticationRequired
	}
	_, account, err := a.account(ctx, repository, tgaccount.ModeRead)
	if err != nil {
		return err
	}
	discovery, ok := account.(humanDiscovery)
	if !ok {
		return ErrTextControlUnsupported
	}
	if err := discovery.EnableReads(ctx, database, authorization.Epoch); err != nil {
		return err
	}
	err = operation(ctx, discovery)
	if errors.Is(err, tgaccount.ErrReauthenticationRequired) {
		return errors.Join(err, repository.InvalidateAuthorization(ctx))
	}
	return err
}

// Grants lists only unexpired authority from the current authorization epoch.
func (a *Application) Grants(ctx context.Context) (grants []policy.Grant, resultError error) {
	resultError = a.withPolicy(ctx, func(lease *policy.Lease) error {
		var err error
		grants, err = lease.List(ctx)
		return err
	})
	if resultError != nil {
		return nil, resultError
	}
	return grants, nil
}

func (a *Application) Grant(ctx context.Context, grant policy.Grant) error {
	return a.withPolicy(ctx, func(lease *policy.Lease) error {
		return lease.Save(ctx, grant)
	})
}

func (a *Application) Revoke(ctx context.Context, peer model.PeerID) error {
	return a.withPolicy(ctx, func(lease *policy.Lease) error {
		return lease.Revoke(ctx, peer)
	})
}

// Policy leases serialize with content delivery without taking the session lock
// or keeping a database transaction open while waiting on Telegram.
func (a *Application) withPolicy(ctx context.Context, operation func(*policy.Lease) error) (resultError error) {
	if a == nil {
		return errors.New("application is not initialized")
	}
	database, _, err := a.openRepository(ctx)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, database.Close()) }()
	policies, err := policy.New(database, filepath.Join(a.paths.StateDir, "policy.lock"), a.now)
	if err != nil {
		return err
	}
	lease, err := policies.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, lease.Close()) }()
	return operation(lease)
}

// FullRead inspects the current account's explicit read-access mode.
func (a *Application) FullRead(ctx context.Context) (enabled bool, err error) {
	err = a.withPolicy(ctx, func(lease *policy.Lease) error {
		enabled, err = lease.FullRead(ctx)
		return err
	})
	return enabled, err
}

func (a *Application) SetFullRead(ctx context.Context, enabled bool) error {
	return a.withPolicy(ctx, func(lease *policy.Lease) error { return lease.SetFullRead(ctx, enabled) })
}
