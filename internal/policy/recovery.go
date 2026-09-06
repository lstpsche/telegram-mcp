package policy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

// RecoverableScope omits identity and authority; restored scopes get new IDs.
type RecoverableScope struct {
	Name  string         `json:"name"`
	Peers []model.PeerID `json:"peers"`
}

func ValidateRecoveryScopes(scopes []RecoverableScope) error {
	if scopes == nil || len(scopes) > MaximumScopes {
		return ErrInvalidScope
	}
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if scope.Peers == nil || seen[scope.Name] {
			return ErrInvalidScope
		}
		if err := validateScope(scope.Name, scope.Peers); err != nil {
			return err
		}
		seen[scope.Name] = true
	}
	return nil
}

// Restore replaces selection metadata while removing authority. Epoch and
// synchronization checkpoints never come from a backup.
func (l *Lease) Restore(ctx context.Context, scopes []RecoverableScope, retention store.AuditRetention) error {
	if err := ValidateRecoveryScopes(scopes); err != nil {
		return err
	}
	if err := retention.Validate(); err != nil {
		return err
	}
	return l.transaction(ctx, func(tx *sql.Tx) error {
		for _, query := range []string{"DELETE FROM full_read_access", "DELETE FROM text_grants", "DELETE FROM named_scopes", "UPDATE policy_revision SET revision=revision+1 WHERE singleton=1"} {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return internalError(err)
			}
		}
		for _, scope := range scopes {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return internalError(err)
			}
			id := "tgscope:v1:" + hex.EncodeToString(random[:])
			if _, err := tx.ExecContext(ctx, "INSERT INTO named_scopes(id,name,authorization_epoch) VALUES(?,?,?)", id, scope.Name, l.epoch); err != nil {
				return internalError(err)
			}
			for _, peer := range scope.Peers {
				if _, err := tx.ExecContext(ctx, "INSERT INTO named_scope_peers(scope_id,peer) VALUES(?,?)", id, peer.String()); err != nil {
					return internalError(err)
				}
			}
		}
		// Explicitly require the binding row even for an empty recovery.
		var revision int64
		if err := tx.QueryRowContext(ctx, "SELECT revision FROM policy_revision WHERE singleton=1").Scan(&revision); err != nil {
			return internalError(err)
		}
		if revision <= 0 {
			return internalError(errors.New("invalid policy revision"))
		}
		return store.SaveAuditRetention(ctx, tx, retention)
	})
}
