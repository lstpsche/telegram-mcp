package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenAppliesEmbeddedMigrationsAndSecurityPragmas(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "state", "telegram-mcp.db")
	database, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { database.Close() })

	info, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %o, want 600", got)
	}
	auxiliaryFiles, err := filepath.Glob(databasePath + "-*")
	if err != nil {
		t.Fatal(err)
	}
	for _, auxiliaryPath := range auxiliaryFiles {
		auxiliaryInfo, err := os.Stat(auxiliaryPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := auxiliaryInfo.Mode().Perm(); got != 0o600 {
			t.Fatalf("SQLite auxiliary file %s mode = %o, want 600", filepath.Base(auxiliaryPath), got)
		}
	}

	assertPragma(t, database, "foreign_keys", "1")
	assertPragma(t, database, "busy_timeout", "5000")
	assertPragma(t, database, "journal_mode", "wal")
	assertPragma(t, database, "trusted_schema", "0")

	var migrationCount int
	if err := database.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 7 {
		t.Fatalf("migration count = %d, want 7", migrationCount)
	}
	var tableCount int
	if err := database.QueryRow(`
        SELECT count(*)
        FROM sqlite_schema
        WHERE type = 'table' AND name IN (
            'schema_migrations',
            'authorization_state',
            'account_config',
            'authentication_checks',
            'text_grants',
            'text_audit',
            'telegram_update_state',
            'telegram_peer_hashes',
            'telegram_channel_state',
            'policy_revision',
            'named_scopes',
            'named_scope_peers'
        )
    `).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 12 {
		t.Fatalf("expected all metadata-only tables, got %d", tableCount)
	}
}

func TestAccountRepositoryRotatesAndInvalidatesAuthorization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "state", "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repository, err := NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}

	configuredAt := fixedMigrationTime()
	config := AccountConfig{
		APIID:       12345,
		Environment: TestEnvironment,
		TestDC:      2,
		UpdatedAt:   configuredAt,
	}
	if err := repository.SaveConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := repository.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || loaded.APIID != config.APIID || loaded.TestDC != config.TestDC || !loaded.UpdatedAt.Equal(configuredAt) {
		t.Fatalf("Config() = %#v, %t", loaded, exists)
	}
	phone := AuthMethodPhone
	if err := repository.RecordAuthorization(ctx, strings.Repeat("x", 43), &phone, 3, configuredAt); !errors.Is(err, ErrInvalidAccountState) {
		t.Fatalf("RecordAuthorization() with mismatched Test DC error = %v", err)
	}

	firstEpoch := strings.Repeat("a", 43)
	if err := repository.RecordAuthorization(ctx, firstEpoch, &phone, 2, configuredAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveConfig(ctx, config); !errors.Is(err, ErrAuthorizationExists) {
		t.Fatalf("SaveConfig() error = %v, want ErrAuthorizationExists", err)
	}

	qr := AuthMethodQR
	secondEpoch := strings.Repeat("b", 43)
	if err := repository.RecordAuthorization(ctx, secondEpoch, &qr, 2, configuredAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	authorization, exists, err := repository.Authorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || authorization.Epoch != secondEpoch || !authorization.CreatedAt.Equal(configuredAt.Add(2*time.Minute)) {
		t.Fatalf("Authorization() = %#v, %t", authorization, exists)
	}
	status, err := repository.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || !status.Authorized || !status.PhoneCheck || !status.QRCheck {
		t.Fatalf("Status() = %#v", status)
	}

	if err := repository.InvalidateAuthorization(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = repository.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Authorized || !status.PhoneCheck || !status.QRCheck {
		t.Fatalf("Status() after invalidation = %#v", status)
	}
	if err := repository.SaveConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	status, err = repository.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PhoneCheck || status.QRCheck {
		t.Fatalf("Status() after reconfiguration retained stale method checks: %#v", status)
	}
	if _, err := database.Exec(`
		INSERT INTO authentication_checks(method, test_dc, authorization_epoch, passed_at)
		VALUES ('phone', 2, ?, 'invalid')
	`, strings.Repeat("c", 43)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Status(ctx); !errors.Is(err, ErrInvalidAccountState) {
		t.Fatalf("Status() with malformed check error = %v", err)
	}
}

func TestMigratorIsIdempotentAndRejectsChangedHistory(t *testing.T) {
	t.Parallel()

	database := openTestDatabase(t)
	files := fstest.MapFS{
		"0001_create_example.sql": &fstest.MapFile{Data: []byte("CREATE TABLE example (id INTEGER PRIMARY KEY) STRICT;")},
	}
	migrator, err := NewMigrator(files, fixedMigrationTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(context.Background(), database); err != nil {
		t.Fatalf("second Apply() error = %v", err)
	}

	changed, err := NewMigrator(fstest.MapFS{
		"0001_create_example.sql": &fstest.MapFile{Data: []byte("CREATE TABLE changed (id INTEGER PRIMARY KEY) STRICT;")},
	}, fixedMigrationTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := changed.Apply(context.Background(), database); err == nil {
		t.Fatal("changed migration history was accepted")
	}
}

func TestMigratorRollsBackFailedMigration(t *testing.T) {
	t.Parallel()

	database := openTestDatabase(t)
	migrator, err := NewMigrator(fstest.MapFS{
		"0001_first.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE first (id INTEGER PRIMARY KEY) STRICT;")},
		"0002_broken.sql": &fstest.MapFile{Data: []byte("CREATE TABLE should_rollback (id INTEGER); invalid SQL;")},
	}, fixedMigrationTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(context.Background(), database); err == nil {
		t.Fatal("broken migration was accepted")
	}

	var tableCount int
	if err := database.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name = 'should_rollback'").Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatal("failed migration left a table behind")
	}
	var versionCount int
	if err := database.QueryRow("SELECT count(*) FROM schema_migrations WHERE version = 2").Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 0 {
		t.Fatal("failed migration was recorded")
	}
}

func TestMigratorRejectsDatabaseFromUnknownSchemaVersion(t *testing.T) {
	t.Parallel()

	database := openTestDatabase(t)
	knownFiles := fstest.MapFS{
		"0001_create_example.sql": &fstest.MapFile{Data: []byte("CREATE TABLE example (id INTEGER PRIMARY KEY) STRICT;")},
	}
	migrator, err := NewMigrator(knownFiles, fixedMigrationTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (2, '0002_future.sql', ?, ?)",
		strings.Repeat("0", 64),
		fixedMigrationTime().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(context.Background(), database); err == nil {
		t.Fatal("database with an unknown migration was accepted")
	}
}

func TestOpenRejectsSymlinkDatabase(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	target := filepath.Join(directory, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if database, err := Open(context.Background(), link); err == nil {
		database.Close()
		t.Fatal("Open() accepted a symlink database")
	}
}

func TestOpenRejectsUnsafePermissions(t *testing.T) {
	t.Parallel()

	t.Run("directory", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stateDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if database, err := Open(context.Background(), filepath.Join(stateDir, "metadata.db")); err == nil {
			_ = database.Close()
			t.Fatal("Open() accepted unsafe directory permissions")
		}
	})

	t.Run("database", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		databasePath := filepath.Join(stateDir, "metadata.db")
		if err := os.WriteFile(databasePath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(databasePath, 0o640); err != nil {
			t.Fatal(err)
		}
		if database, err := Open(context.Background(), databasePath); err == nil {
			_ = database.Close()
			t.Fatal("Open() accepted unsafe database permissions")
		}
	})
}

func TestLoadMigrationsRejectsMalformedSets(t *testing.T) {
	t.Parallel()

	tests := map[string]fs.FS{
		"invalid filename": fstest.MapFS{"first.sql": &fstest.MapFile{Data: []byte("SELECT 1;")}},
		"zero version":     fstest.MapFS{"0000_zero.sql": &fstest.MapFile{Data: []byte("SELECT 1;")}},
		"empty migration":  fstest.MapFS{"0001_empty.sql": &fstest.MapFile{Data: nil}},
		"nested directory": fstest.MapFS{"nested": &fstest.MapFile{Mode: fs.ModeDir}},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := loadMigrations(files); err == nil {
				t.Fatal("loadMigrations() accepted invalid set")
			}
		})
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "migration.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { database.Close() })
	return database
}

func assertPragma(t *testing.T, database *sql.DB, name, expected string) {
	t.Helper()
	var actual string
	if err := database.QueryRow("PRAGMA " + name).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("PRAGMA %s = %q, want %q", name, actual, expected)
	}
}

func fixedMigrationTime() time.Time {
	return time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)
}
