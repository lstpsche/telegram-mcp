// Package store owns Telegram MCP's metadata-only SQLite boundary and migrations.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL UNIQUE,
    checksum TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL
) STRICT;
`

var migrationNamePattern = regexp.MustCompile(`^([0-9]{4})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)

type migration struct {
	version  int64
	name     string
	contents string
	checksum string
}

// Migrator applies one checksum-verified transaction per ordered migration.
type Migrator struct {
	files fs.FS
	now   func() time.Time
}

func NewMigrator(files fs.FS, now func() time.Time) (*Migrator, error) {
	if files == nil {
		return nil, errors.New("migration filesystem is required")
	}
	if now == nil {
		return nil, errors.New("migration clock is required")
	}
	return &Migrator{files: files, now: now}, nil
}

func (m *Migrator) Apply(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("migration database is required")
	}
	migrations, err := loadMigrations(m.files)
	if err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("create schema migration table: %w", err)
	}
	if err := rejectUnknownAppliedMigrations(ctx, database, migrations); err != nil {
		return err
	}
	for _, next := range migrations {
		if err := m.applyOne(ctx, database, next); err != nil {
			return err
		}
	}
	return nil
}

func rejectUnknownAppliedMigrations(ctx context.Context, database *sql.DB, migrations []migration) error {
	known := make(map[int64]struct{}, len(migrations))
	for _, migration := range migrations {
		known[migration.version] = struct{}{}
	}

	rows, err := database.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("inspect applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("inspect applied migration version: %w", err)
		}
		if _, exists := known[version]; !exists {
			return fmt.Errorf("database contains unknown migration %04d", version)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect applied migrations: %w", err)
	}
	return nil
}

func (m *Migrator) applyOne(ctx context.Context, database *sql.DB, next migration) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", next.name, err)
	}
	defer transaction.Rollback()

	var existingName string
	var existingChecksum string
	err = transaction.QueryRowContext(
		ctx,
		"SELECT name, checksum FROM schema_migrations WHERE version = ?",
		next.version,
	).Scan(&existingName, &existingChecksum)
	switch {
	case err == nil:
		if existingName != next.name || existingChecksum != next.checksum {
			return fmt.Errorf("migration %04d changed after it was applied", next.version)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration check %s: %w", next.name, err)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("inspect migration %s: %w", next.name, err)
	}

	appliedAt := m.now()
	if appliedAt.IsZero() {
		return fmt.Errorf("apply migration %s: migration clock returned zero time", next.name)
	}
	if _, err := transaction.ExecContext(ctx, next.contents); err != nil {
		return fmt.Errorf("apply migration %s: %w", next.name, err)
	}
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
		next.version,
		next.name,
		next.checksum,
		appliedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record migration %s: %w", next.name, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", next.name, err)
	}
	return nil
}

func loadMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	loaded := make([]migration, 0, len(entries))
	seenVersions := make(map[int64]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("migration directory %q is not allowed", entry.Name())
		}
		matches := migrationNamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if previous, exists := seenVersions[version]; exists {
			return nil, fmt.Errorf("migration version %04d is used by %q and %q", version, previous, entry.Name())
		}
		contents, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			return nil, fmt.Errorf("migration %q is empty", entry.Name())
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(contents))
		loaded = append(loaded, migration{
			version:  version,
			name:     entry.Name(),
			contents: string(contents),
			checksum: checksum,
		})
		seenVersions[version] = entry.Name()
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].version < loaded[j].version })
	return loaded, nil
}
