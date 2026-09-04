package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
	"golang.org/x/sys/unix"
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

	databaseURL := &url.URL{Scheme: "file", Path: absolutePath}
	query := databaseURL.Query()
	query.Set("_defensive", "1")
	query.Set("_busy_timeout", fmt.Sprint(busyTimeoutMilliseconds))
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "wal")
	query.Set("_synchronous", "full")
	query.Set("_txlock", "immediate")
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
	if err := migrator.Apply(ctx, database); err != nil {
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
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create metadata directory: %w", err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return "", fmt.Errorf("inspect metadata directory: %w", err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("metadata directory must be a real directory")
	}
	var parentStatus unix.Stat_t
	if err := unix.Lstat(parent, &parentStatus); err != nil {
		return "", fmt.Errorf("inspect metadata directory owner: %w", err)
	}
	if parentStatus.Uid != uint32(os.Geteuid()) {
		return "", errors.New("metadata directory must be owned by the current user")
	}
	if parentInfo.Mode().Perm() != 0o700 {
		return "", errors.New("metadata directory permissions must be 0700")
	}

	fileDescriptor, err := unix.Open(
		absolutePath,
		unix.O_CLOEXEC|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_RDWR,
		0o600,
	)
	if err != nil {
		return "", fmt.Errorf("open private metadata database: %w", err)
	}
	var fileStatus unix.Stat_t
	statusError := unix.Fstat(fileDescriptor, &fileStatus)
	closeError := unix.Close(fileDescriptor)
	if statusError != nil {
		return "", fmt.Errorf("inspect metadata database descriptor: %w", statusError)
	}
	if closeError != nil {
		return "", fmt.Errorf("close metadata database descriptor: %w", closeError)
	}
	if fileStatus.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", errors.New("metadata database must be a regular file")
	}
	if fileStatus.Uid != uint32(os.Geteuid()) {
		return "", errors.New("metadata database must be owned by the current user")
	}
	if os.FileMode(fileStatus.Mode).Perm() != 0o600 {
		return "", errors.New("metadata database permissions must be 0600")
	}
	return absolutePath, nil
}
