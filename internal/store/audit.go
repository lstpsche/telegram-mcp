package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidRetention = errors.New("invalid audit retention")

type AuditRetention struct {
	Days       int `json:"days"`
	MaxRecords int `json:"max_records"`
}

func (r AuditRetention) Validate() error {
	if r.Days < 1 || r.Days > 3650 || r.MaxRecords < 1 || r.MaxRecords > 1000000 {
		return ErrInvalidRetention
	}
	return nil
}

type AuditStatus struct {
	Retention AuditRetention `json:"retention"`
	Records   int64          `json:"records"`
	Prunable  int64          `json:"prunable"`
}

func ReadAuditRetention(ctx context.Context, tx *sql.Tx) (AuditRetention, error) {
	var r AuditRetention
	if err := tx.QueryRowContext(ctx, "SELECT days,max_records FROM audit_retention WHERE singleton=1").Scan(&r.Days, &r.MaxRecords); err != nil {
		return r, fmt.Errorf("read audit retention: %w", err)
	}
	return r, r.Validate()
}

func SaveAuditRetention(ctx context.Context, tx *sql.Tx, r AuditRetention) error {
	if err := r.Validate(); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE audit_retention SET days=?,max_records=? WHERE singleton=1", r.Days, r.MaxRecords)
	if err != nil {
		return fmt.Errorf("save audit retention: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("audit retention is missing")
	}
	return nil
}

// All writers use UTC RFC3339Nano. A suffix-free whole-second cutoff sorts
// before both fractional and integral timestamps in that second.
func auditCutoff(now time.Time, days int) string {
	return now.UTC().Truncate(time.Second).AddDate(0, 0, -days).Format("2006-01-02T15:04:05")
}

const retainedAudit = `SELECT id FROM text_audit WHERE recorded_at >= ? ORDER BY id DESC LIMIT ?`

func InspectAudit(ctx context.Context, tx *sql.Tx, now time.Time) (AuditStatus, error) {
	r, err := ReadAuditRetention(ctx, tx)
	if err != nil {
		return AuditStatus{}, err
	}
	status := AuditStatus{Retention: r}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM text_audit").Scan(&status.Records); err != nil {
		return AuditStatus{}, fmt.Errorf("count audit records: %w", err)
	}
	var retained int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM ("+retainedAudit+")", auditCutoff(now, r.Days), r.MaxRecords).Scan(&retained); err != nil {
		return AuditStatus{}, fmt.Errorf("count retained audit records: %w", err)
	}
	status.Prunable = status.Records - retained
	return status, nil
}

// PruneAudit participates in the caller's transaction, including required
// audit insertion. Failure rolls back both deletion and insertion.
func PruneAudit(ctx context.Context, tx *sql.Tx, now time.Time) (int64, error) {
	r, err := ReadAuditRetention(ctx, tx)
	if err != nil {
		return 0, err
	}
	ageResult, err := tx.ExecContext(ctx, "DELETE FROM text_audit WHERE recorded_at < ?", auditCutoff(now, r.Days))
	if err != nil {
		return 0, fmt.Errorf("prune expired audit records: %w", err)
	}
	ageCount, err := ageResult.RowsAffected()
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM text_audit WHERE id < (SELECT id FROM text_audit ORDER BY id DESC LIMIT 1 OFFSET ?)", r.MaxRecords-1)
	if err != nil {
		return 0, fmt.Errorf("prune excess audit records: %w", err)
	}
	count, err := result.RowsAffected()
	return ageCount + count, err
}
