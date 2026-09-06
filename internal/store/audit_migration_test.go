package store

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
)

func TestAuditRetentionMigrationPreservesExistingRows(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0011_" {
			continue
		}
		data, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		prior[entry.Name()] = &fstest.MapFile{Data: data}
	}
	old, err := NewMigrator(prior, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO text_audit VALUES(9,'req_old','catch_up','success','',1,0,'2020-01-01T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	current, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var id int
	var recorded string
	if err := db.QueryRow("SELECT id,recorded_at FROM text_audit").Scan(&id, &recorded); err != nil || id != 9 || recorded != "2020-01-01T00:00:00Z" {
		t.Fatal("migration pruned existing audit", err)
	}
	for _, query := range []string{"UPDATE audit_retention SET days=0", "UPDATE audit_retention SET days=3651", "UPDATE audit_retention SET max_records=0", "UPDATE audit_retention SET max_records=1000001", "INSERT INTO audit_retention VALUES(2,30,100)"} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("invalid retention accepted", query)
		}
	}
	if err := current.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
}
