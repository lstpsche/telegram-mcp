package store

import (
	"context"
	"database/sql"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
)

func TestSendingMigrationPreservesReadAuthorityAndAddsNoSendPermission(t *testing.T) {
	db := openTestDatabase(t)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0015_" {
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
	ctx := context.Background()
	if err := old.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	epoch := strings.Repeat("a", 43)
	if _, err := db.Exec(`INSERT INTO authorization_state VALUES(1,?,'2026-11-17T00:00:00Z','2026-11-17T00:00:00Z')`, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO full_read_access VALUES(1,?)`, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO text_grants(peer,authorization_epoch,author,min_id,max_id,read_through,profile,expires_at,eligible,images,documents,voice_notes) VALUES('tgpeer:v1:chat:1',?,'tgpeer:v1:user:2',1,20,15,'consented','2026-11-18T00:00:00Z',1,1,1,1)`, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO named_scopes(id,name,authorization_epoch) VALUES('tgscope:v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','work',?)`, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO named_scope_peers VALUES('tgscope:v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','tgpeer:v1:chat:1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO text_audit(request_id,operation,outcome,category,item_count,uncertain,recorded_at) VALUES('req_existing','list_messages','success','',1,0,'2026-11-17T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	preservedQueries := []string{
		"SELECT * FROM text_grants ORDER BY peer",
		"SELECT * FROM named_scopes ORDER BY id",
		"SELECT * FROM named_scope_peers ORDER BY scope_id,peer",
		"SELECT * FROM text_audit ORDER BY id",
		"SELECT * FROM schema_migrations WHERE version < 15 ORDER BY version",
	}
	preserved := make(map[string][][]any, len(preservedQueries))
	for _, query := range preservedQueries {
		preserved[query] = sendingMigrationRows(t, db, query)
	}
	var before int64
	if err := db.QueryRow("SELECT revision FROM policy_revision").Scan(&before); err != nil {
		t.Fatal(err)
	}
	current, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := current.Apply(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range preservedQueries {
		if after := sendingMigrationRows(t, db, query); !reflect.DeepEqual(after, preserved[query]) {
			t.Fatal("upgrade changed existing metadata", query)
		}
	}
	for _, table := range []string{"full_send_access", "outgoing_operations", "telegram_rpc_waits"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("upgrade introduced send authority or work", table, count, err)
		}
	}
	var after int64
	if err := db.QueryRow("SELECT revision FROM policy_revision").Scan(&after); err != nil || after != before {
		t.Fatal("upgrade changed policy revision", err)
	}
	var fullRead int
	if err := db.QueryRow("SELECT count(*) FROM full_read_access WHERE authorization_epoch=?", epoch).Scan(&fullRead); err != nil || fullRead != 1 {
		t.Fatal("upgrade changed read authority", err)
	}
	for _, table := range []string{"outgoing_operations", "telegram_rpc_waits"} {
		rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		if rows.Next() {
			rows.Close()
			t.Fatal("retained metadata must not depend on authorization lifetime", table)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
}

func TestOutgoingSchemaContainsOnlyFixedContentFreeMetadata(t *testing.T) {
	db := openTestDatabase(t)
	migrator, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{
		"full_send_access":    {"singleton", "authorization_epoch"},
		"outgoing_operations": {"operation_id", "authorization_epoch", "peer", "reply_to", "payload_digest", "random_id", "state", "message_id", "sent_at", "failure_code", "retry_at", "created_at", "updated_at", "retain_until"},
		"telegram_rpc_waits":  {"environment", "account_id", "method", "scope_peer", "not_before"},
	}
	for table, columns := range expected {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		index := 0
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if index >= len(columns) || name != columns[index] {
				rows.Close()
				t.Fatal("unexpected metadata column", table, name)
			}
			index++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if index != len(columns) {
			t.Fatal("missing metadata columns", table, index)
		}
	}
}

func sendingMigrationRows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := make([][]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
