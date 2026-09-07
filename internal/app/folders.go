package app

import (
	"context"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

// Folders lists folders, or resolves one folder's current membership when id is nonzero.
func (a *Application) Folders(ctx context.Context, id int32) (folders []model.Folder, err error) {
	err = a.withDiscovery(ctx, func(ctx context.Context, discovery humanDiscovery) error {
		folders, err = discoverFolders(ctx, discovery, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return folders, nil
}

func discoverFolders(ctx context.Context, discovery humanDiscovery, id int32) ([]model.Folder, error) {
	folders, ok := discovery.(interface {
		DiscoverFolders(context.Context, int32) ([]model.Folder, error)
	})
	if !ok {
		return nil, ErrTextControlUnsupported
	}
	return folders.DiscoverFolders(ctx, id)
}

// ScopeFromFolder saves a one-time selection while retaining session ownership
// across discovery and the policy transaction. No grants or folder data are saved.
func (a *Application) ScopeFromFolder(ctx context.Context, id model.ScopeID, name string, folder int32) (scope policy.Scope, err error) {
	if folder < 2 || !model.ValidScopeName(name) || (id != "" && id.String() == "") {
		return scope, model.TextError(model.ErrorInvalidInput, nil)
	}
	err = a.withDiscovery(ctx, func(ctx context.Context, discovery humanDiscovery) error {
		folders, err := discoverFolders(ctx, discovery, folder)
		if err != nil {
			return err
		}
		if len(folders) != 1 || folders[0].ID != folder {
			return model.TextError(model.ErrorInvalidReference, nil)
		}
		if len(folders[0].Peers) > policy.MaximumScopePeers {
			return model.TextError(model.ErrorResultTooLarge, policy.ErrInvalidScope)
		}
		return a.withPolicy(ctx, func(lease *policy.Lease) error {
			scope, err = lease.SaveScope(ctx, id, name, folders[0].Peers)
			return err
		})
	})
	if err != nil {
		return policy.Scope{}, err
	}
	return scope, nil
}
