package policy

import (
	"context"
	"database/sql"
	"errors"
)

func fullReadEnabled(ctx context.Context, tx *sql.Tx, epoch string) (bool, error) {
	var stored string
	err := tx.QueryRowContext(ctx, "SELECT authorization_epoch FROM full_read_access WHERE singleton = 1").Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, internalError(err)
	}
	if stored != epoch {
		return false, internalError(ErrEpochChanged)
	}
	return true, nil
}

func (l *Lease) FullRead(ctx context.Context) (bool, error) {
	var enabled bool
	err := l.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		enabled, err = fullReadEnabled(ctx, tx, l.epoch)
		return err
	})
	return enabled, err
}

// SetFullRead changes account authority under the same lease as content release.
// Existing restricted grants are preserved. Triggers invalidate issued tokens.
func (l *Lease) SetFullRead(ctx context.Context, enabled bool) error {
	return l.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		if enabled {
			_, err = tx.ExecContext(ctx, `INSERT INTO full_read_access VALUES (1,?)
    ON CONFLICT(singleton) DO UPDATE SET authorization_epoch=excluded.authorization_epoch`, l.epoch)
		} else {
			_, err = tx.ExecContext(ctx, "DELETE FROM full_read_access WHERE singleton = 1")
		}
		if err != nil {
			return internalError(err)
		}
		return nil
	})
}
