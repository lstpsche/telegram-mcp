package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyReadsWALAndRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata", "account.db")
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, "CREATE TABLE readonly_probe(value TEXT); INSERT INTO readonly_probe VALUES ('current')"); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var value string
	if err := reader.QueryRowContext(ctx, "SELECT value FROM readonly_probe").Scan(&value); err != nil || value != "current" {
		t.Fatalf("read committed WAL data: value=%q err=%v", value, err)
	}
	if _, err := reader.ExecContext(ctx, "DELETE FROM readonly_probe"); err == nil {
		t.Fatal("read-only database accepted a write")
	}
}

func TestOpenReadOnlyRejectsIncompatibleSchemaWithoutChangingDatabase(t *testing.T) {
	for _, mutation := range []string{
		"DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)",
		"UPDATE schema_migrations SET checksum = printf('%064d', 0)",
		"INSERT INTO schema_migrations VALUES (9999, 'unknown', printf('%064d', 0), 'now')",
	} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "metadata", "account.db")
			db, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, mutation); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := OpenReadOnly(ctx, path)
			if err == nil {
				reader.Close()
				t.Fatal("accepted incompatible migration history")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("read-only open modified database")
			}
		})
	}
}
