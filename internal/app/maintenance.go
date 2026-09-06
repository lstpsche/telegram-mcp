package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

var ErrBackupEnvironment = errors.New("backup environment does not match configured account")

// withMaintenance owns both locks and opens metadata before invoking the operation.
func (a *Application) withMaintenance(ctx context.Context, operation func(context.Context, *sql.DB, *store.Repository) error) (resultError error) {
	if a == nil {
		return errors.New("application is not initialized")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	accountLock, err := daemon.AcquireAccountLock(a.paths.Lock)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, accountLock.Release()) }()
	policyLock, err := daemon.AcquireAccountLock(filepath.Join(a.paths.StateDir, "policy.lock"))
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, policyLock.Release()) }()
	db, repository, err := a.openRepository(ctx)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, db.Close()) }()
	return operation(ctx, db, repository)
}

// Recovery uses the policy lease inside the exclusive account lock. Unlike
// audit maintenance it requires a current authorization epoch.
func (a *Application) withRecovery(ctx context.Context, operation func(context.Context, *policy.Lease, *store.Repository, *sql.DB) error) (resultError error) {
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
	db, repository, err := a.openRepository(ctx)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, db.Close()) }()
	policies, err := policy.New(db, filepath.Join(a.paths.StateDir, "policy.lock"), a.now)
	if err != nil {
		return err
	}
	lease, err := policies.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, lease.Close()) }()
	return operation(ctx, lease, repository, db)
}

func (a *Application) Backup(ctx context.Context, path string) error {
	return a.withRecovery(ctx, func(ctx context.Context, lease *policy.Lease, repository *store.Repository, db *sql.DB) error {
		config, exists, err := repository.Config(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return ErrConfigurationRequired
		}
		scopes, err := lease.Scopes(ctx)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		retention, err := store.ReadAuditRetention(ctx, tx)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		backup := MetadataBackup{Version: 1, Environment: config.Environment, TestDC: config.TestDC, Retention: retention, Scopes: make([]policy.RecoverableScope, 0, len(scopes))}
		for _, scope := range scopes {
			backup.Scopes = append(backup.Scopes, policy.RecoverableScope{Name: scope.Name, Peers: scope.Peers})
		}
		return writeBackup(path, backup)
	})
}

func (a *Application) Restore(ctx context.Context, path string) error {
	backup, err := InspectBackup(path)
	if err != nil {
		return err
	}
	return a.withRecovery(ctx, func(ctx context.Context, lease *policy.Lease, repository *store.Repository, _ *sql.DB) error {
		config, exists, err := repository.Config(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return ErrConfigurationRequired
		}
		if config.Environment != backup.Environment || config.TestDC != backup.TestDC {
			return ErrBackupEnvironment
		}
		return lease.Restore(ctx, backup.Scopes, backup.Retention)
	})
}

// AuditMaintenance returns post-transaction counts. A nil retention inspects;
// apply prunes under the selected retention; purge removes all audit records.
func (a *Application) AuditMaintenance(ctx context.Context, retention *store.AuditRetention, apply, purge bool) (status store.AuditStatus, resultError error) {
	if retention != nil {
		if err := retention.Validate(); err != nil {
			return status, err
		}
	}
	if (retention != nil && !apply) || (purge && (apply || retention != nil)) {
		return status, store.ErrInvalidRetention
	}
	resultError = a.withMaintenance(ctx, func(ctx context.Context, db *sql.DB, _ *store.Repository) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if retention != nil {
			if err := store.SaveAuditRetention(ctx, tx, *retention); err != nil {
				return err
			}
		}
		if apply {
			if _, err := store.PruneAudit(ctx, tx, a.now()); err != nil {
				return err
			}
		}
		if purge {
			if _, err := tx.ExecContext(ctx, "DELETE FROM text_audit"); err != nil {
				return err
			}
		}
		status, err = store.InspectAudit(ctx, tx, a.now())
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	if resultError != nil {
		return store.AuditStatus{}, resultError
	}
	return status, nil
}
