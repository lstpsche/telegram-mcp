package app

import (
	"context"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

// Scopes lists human-managed membership without granting access to its peers.
func (a *Application) Scopes(ctx context.Context) (scopes []policy.Scope, resultError error) {
	resultError = a.withPolicy(ctx, func(lease *policy.Lease) error {
		var err error
		scopes, err = lease.Scopes(ctx)
		return err
	})
	if resultError != nil {
		return nil, resultError
	}
	return scopes, nil
}

// Scope atomically creates or replaces a named membership in the current epoch.
func (a *Application) Scope(ctx context.Context, id model.ScopeID, name string, peers []model.PeerID) (scope policy.Scope, resultError error) {
	resultError = a.withPolicy(ctx, func(lease *policy.Lease) error {
		var err error
		scope, err = lease.SaveScope(ctx, id, name, peers)
		return err
	})
	if resultError != nil {
		return policy.Scope{}, resultError
	}
	return scope, nil
}

func (a *Application) Unscope(ctx context.Context, id model.ScopeID) error {
	return a.withPolicy(ctx, func(lease *policy.Lease) error {
		return lease.DeleteScope(ctx, id)
	})
}
