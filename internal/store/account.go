package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var (
	ErrAuthorizationExists = errors.New("authorization must be removed before changing account configuration")
	ErrInvalidAccountState = errors.New("account metadata is invalid")
)

const (
	TestEnvironment       = "test"
	ProductionEnvironment = "production"
)

type AuthMethod string

const (
	AuthMethodPhone AuthMethod = "phone"
	AuthMethodQR    AuthMethod = "qr"
)

func (m AuthMethod) valid() bool {
	return m == AuthMethodPhone || m == AuthMethodQR
}

type AccountConfig struct {
	APIID       int
	Environment string
	TestDC      int
	UpdatedAt   time.Time
}

type AuthorizationState struct {
	Epoch     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type AccountStatus struct {
	Configured bool
	Config     AccountConfig
	Authorized bool
	PhoneCheck bool
	QRCheck    bool
}

type Repository struct {
	database *sql.DB
}

func NewRepository(database *sql.DB) (*Repository, error) {
	if database == nil {
		return nil, errors.New("account repository database is required")
	}
	return &Repository{database: database}, nil
}

func (r *Repository) Config(ctx context.Context) (AccountConfig, bool, error) {
	if r == nil || r.database == nil {
		return AccountConfig{}, false, errors.New("account repository is not initialized")
	}
	var config AccountConfig
	var updatedAt string
	err := r.database.QueryRowContext(ctx, `
		SELECT api_id, environment, test_dc, updated_at
		FROM account_config
		WHERE singleton = 1
	`).Scan(&config.APIID, &config.Environment, &config.TestDC, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountConfig{}, false, nil
	}
	if err != nil {
		return AccountConfig{}, false, fmt.Errorf("read account configuration: %w", err)
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return AccountConfig{}, false, ErrInvalidAccountState
	}
	config.UpdatedAt = parsedTime.UTC()
	if err := validateConfig(config); err != nil {
		return AccountConfig{}, false, err
	}
	return config, true, nil
}

func (r *Repository) SaveConfig(ctx context.Context, config AccountConfig) error {
	if r == nil || r.database == nil {
		return errors.New("account repository is not initialized")
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin account configuration: %w", err)
	}
	defer transaction.Rollback()
	var authorizationCount int
	if err := transaction.QueryRowContext(ctx, "SELECT count(*) FROM authorization_state").Scan(&authorizationCount); err != nil {
		return fmt.Errorf("inspect authorization before configuration: %w", err)
	}
	if authorizationCount != 0 {
		return ErrAuthorizationExists
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM authentication_checks"); err != nil {
		return fmt.Errorf("reset authentication checks: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO account_config(singleton, api_id, environment, test_dc, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			api_id = excluded.api_id,
			environment = excluded.environment,
			test_dc = excluded.test_dc,
			updated_at = excluded.updated_at
	`, config.APIID, config.Environment, config.TestDC, config.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("save account configuration: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit account configuration: %w", err)
	}
	return nil
}

func (r *Repository) Authorization(ctx context.Context) (AuthorizationState, bool, error) {
	if r == nil || r.database == nil {
		return AuthorizationState{}, false, errors.New("account repository is not initialized")
	}
	var state AuthorizationState
	var createdAt string
	var updatedAt string
	err := r.database.QueryRowContext(ctx, `
		SELECT epoch, created_at, updated_at
		FROM authorization_state
		WHERE singleton = 1
	`).Scan(&state.Epoch, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationState{}, false, nil
	}
	if err != nil {
		return AuthorizationState{}, false, fmt.Errorf("read authorization state: %w", err)
	}
	state.CreatedAt, err = parseMetadataTime(createdAt)
	if err != nil {
		return AuthorizationState{}, false, err
	}
	state.UpdatedAt, err = parseMetadataTime(updatedAt)
	if err != nil {
		return AuthorizationState{}, false, err
	}
	if !epochPattern.MatchString(state.Epoch) {
		return AuthorizationState{}, false, ErrInvalidAccountState
	}
	return state, true, nil
}

// RecordAuthorization atomically rotates the authorization epoch and, when a
// method is supplied, records the corresponding environment-bound authentication check.
func (r *Repository) RecordAuthorization(
	ctx context.Context,
	epoch string,
	method *AuthMethod,
	testDC int,
	at time.Time,
) error {
	if r == nil || r.database == nil {
		return errors.New("account repository is not initialized")
	}
	if !epochPattern.MatchString(epoch) || at.IsZero() || testDC < 0 || testDC > 3 {
		return ErrInvalidAccountState
	}
	if method != nil && !method.valid() {
		return ErrInvalidAccountState
	}
	timestamp := at.UTC().Format(time.RFC3339Nano)
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin authorization update: %w", err)
	}
	defer transaction.Rollback()
	var configuredTestDC int
	var environment string
	if err := transaction.QueryRowContext(ctx, `
		SELECT environment, test_dc
		FROM account_config
		WHERE singleton = 1
	`).Scan(&environment, &configuredTestDC); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidAccountState
		}
		return fmt.Errorf("read account configuration for authorization: %w", err)
	}
	if configuredTestDC != testDC {
		return ErrInvalidAccountState
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO authorization_state(singleton, epoch, created_at, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			epoch = excluded.epoch,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at
	`, epoch, timestamp, timestamp); err != nil {
		return fmt.Errorf("record authorization epoch: %w", err)
	}
	if method != nil {
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO authentication_checks(method, environment, test_dc, authorization_epoch, passed_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(method) DO UPDATE SET
				environment = excluded.environment,
				test_dc = excluded.test_dc,
				authorization_epoch = excluded.authorization_epoch,
				passed_at = excluded.passed_at
		`, string(*method), environment, testDC, epoch, timestamp); err != nil {
			return fmt.Errorf("record authentication check: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit authorization update: %w", err)
	}
	return nil
}

func (r *Repository) InvalidateAuthorization(ctx context.Context) error {
	if r == nil || r.database == nil {
		return errors.New("account repository is not initialized")
	}
	if _, err := r.database.ExecContext(ctx, "DELETE FROM authorization_state WHERE singleton = 1"); err != nil {
		return fmt.Errorf("invalidate authorization state: %w", err)
	}
	return nil
}

func (r *Repository) Status(ctx context.Context) (AccountStatus, error) {
	config, configured, err := r.Config(ctx)
	if err != nil {
		return AccountStatus{}, err
	}
	_, authorized, err := r.Authorization(ctx)
	if err != nil {
		return AccountStatus{}, err
	}
	status := AccountStatus{Configured: configured, Config: config, Authorized: authorized}
	rows, err := r.database.QueryContext(ctx, `
		SELECT method, environment, test_dc, authorization_epoch, passed_at
		FROM authentication_checks
	`)
	if err != nil {
		return AccountStatus{}, fmt.Errorf("read authentication checks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var method AuthMethod
		var testDC int
		var environment string
		var authorizationEpoch string
		var passedAt string
		if err := rows.Scan(&method, &environment, &testDC, &authorizationEpoch, &passedAt); err != nil {
			return AccountStatus{}, fmt.Errorf("read authentication check: %w", err)
		}
		if !method.valid() || !configured || testDC != config.TestDC || environment != config.Environment ||
			!epochPattern.MatchString(authorizationEpoch) {
			return AccountStatus{}, ErrInvalidAccountState
		}
		if _, err := parseMetadataTime(passedAt); err != nil {
			return AccountStatus{}, err
		}
		switch method {
		case AuthMethodPhone:
			status.PhoneCheck = true
		case AuthMethodQR:
			status.QRCheck = true
		}
	}
	if err := rows.Err(); err != nil {
		return AccountStatus{}, fmt.Errorf("read authentication checks: %w", err)
	}
	return status, nil
}

var epochPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22,128}$`)

func validateConfig(config AccountConfig) error {
	if config.APIID <= 0 || int64(config.APIID) > int64(1<<31-1) ||
		!((config.Environment == TestEnvironment && config.TestDC >= 1 && config.TestDC <= 3) ||
			(config.Environment == ProductionEnvironment && config.TestDC == 0)) ||
		config.UpdatedAt.IsZero() {
		return ErrInvalidAccountState
	}
	return nil
}

func parseMetadataTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, ErrInvalidAccountState
	}
	return parsed.UTC(), nil
}
