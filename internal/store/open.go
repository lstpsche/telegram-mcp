package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/migrations"
	_ "modernc.org/sqlite"
)

const busyTimeoutMilliseconds = 5000

// Open creates or opens a private, metadata-only SQLite database and applies
// all embedded migrations before returning it.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	absolutePath, err := prepareDatabasePath(path)
	if err != nil {
		return nil, err
	}

	return openDatabase(ctx, absolutePath, false)
}

// OpenReadOnly opens existing metadata without creating it or applying migrations.
// The schema must already match the embedded migrations.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("metadata database path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve metadata database path: %w", err)
	}
	if err := privatefs.CheckDirectory(filepath.Dir(absolutePath)); err != nil {
		return nil, fmt.Errorf("inspect metadata directory: %w", err)
	}
	if err := privatefs.CheckFile(absolutePath); err != nil {
		return nil, fmt.Errorf("inspect metadata database: %w", err)
	}
	return openDatabase(ctx, absolutePath, true)
}

func openDatabase(ctx context.Context, absolutePath string, readOnly bool) (*sql.DB, error) {
	uriPath := filepath.ToSlash(absolutePath)
	if filepath.VolumeName(absolutePath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	databaseURL := &url.URL{Scheme: "file", Path: uriPath}
	query := databaseURL.Query()
	query.Set("_defensive", "1")
	query.Set("_busy_timeout", fmt.Sprint(busyTimeoutMilliseconds))
	query.Set("_foreign_keys", "on")
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query.Set("_journal_mode", "wal")
		query.Set("_synchronous", "full")
		query.Set("_txlock", "immediate")
	}
	query.Add("_pragma", "trusted_schema(OFF)")
	databaseURL.RawQuery = query.Encode()

	database, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, fmt.Errorf("open metadata database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	closeWithError := func(openError error) (*sql.DB, error) {
		if closeError := database.Close(); closeError != nil {
			return nil, errors.Join(openError, fmt.Errorf("close metadata database: %w", closeError))
		}
		return nil, openError
	}
	if err := database.PingContext(ctx); err != nil {
		return closeWithError(fmt.Errorf("connect metadata database: %w", err))
	}
	migrator, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		return closeWithError(err)
	}
	if readOnly {
		err = migrator.check(ctx, database)
	} else {
		err = migrator.Apply(ctx, database)
	}
	if err != nil {
		return closeWithError(err)
	}
	return database, nil
}

func prepareDatabasePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("metadata database path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve metadata database path: %w", err)
	}
	parent := filepath.Dir(absolutePath)
	if err := privatefs.EnsureDirectory(parent); err != nil {
		return "", fmt.Errorf("prepare metadata directory: %w", err)
	}
	if err := privatefs.CheckFile(absolutePath); errors.Is(err, os.ErrNotExist) {
		if err := privatefs.WriteFile(absolutePath, []byte{}, false); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create metadata database: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect metadata database: %w", err)
	}
	if err := privatefs.CheckFile(absolutePath); err != nil {
		return "", fmt.Errorf("inspect metadata database: %w", err)
	}
	return absolutePath, nil
}
