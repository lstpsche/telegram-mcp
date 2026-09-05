package store

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lstpsche/telegram-mcp/migrations"
)

func TestAccountMigrationPreservesTestAuthorityAndSeparatesEnvironments(t *testing.T) {
	database := openTestDatabase(t)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0008_" {
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
	if err := old.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	epoch, at := strings.Repeat("a", 43), fixedMigrationTime()
	for _, statement := range []string{
		"INSERT INTO account_config VALUES(1,12345,'test',2,'2026-09-05T00:00:00Z')",
		"INSERT INTO authorization_state VALUES(1,?,'2026-09-05T00:00:00Z','2026-09-05T00:00:00Z')",
		"INSERT INTO authentication_checks VALUES('phone',2,?,'2026-09-05T00:00:00Z')",
		"INSERT INTO telegram_update_state VALUES(?,1,10,0,100,1)",
		"INSERT INTO telegram_peer_hashes VALUES(?,1,'user',2,22)",
		"INSERT INTO telegram_peer_hashes VALUES(?,1,'channel',3,33)",
		"INSERT INTO telegram_channel_state VALUES(?,1,3,900)",
		"INSERT INTO text_grants VALUES('tgpeer:v1:user:2',?,'tgpeer:v1:user:2',1,20,20,'consented','2026-09-06T00:00:00Z',1,0)",
	} {
		var args []any
		if strings.Contains(statement, "?") {
			args = []any{epoch}
		}
		if _, err := database.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	current, err := NewMigrator(migrations.Files, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	status, err := repository.Status(ctx)
	if err != nil || !status.Authorized || !status.PhoneCheck || status.QRCheck || status.Config.Environment != TestEnvironment || status.Config.TestDC != 2 {
		t.Fatal("migration lost test authorization", err)
	}
	for query, want := range map[string]int{
		"SELECT pts FROM telegram_update_state":                          10,
		"SELECT count(*) FROM text_grants":                               1,
		"SELECT count(*) FROM telegram_peer_hashes WHERE kind='user'":    1,
		"SELECT count(*) FROM telegram_peer_hashes WHERE kind='channel'": 0,
		"SELECT count(*) FROM telegram_channel_state":                    0,
	} {
		var got int
		if err := database.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatal("migration changed retained metadata", query, err)
		}
	}
	if err := repository.InvalidateAuthorization(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveConfig(ctx, AccountConfig{APIID: 12345, Environment: ProductionEnvironment, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	phone := AuthMethodPhone
	if err := repository.RecordAuthorization(ctx, strings.Repeat("b", 43), &phone, 0, at); err != nil {
		t.Fatal(err)
	}
	status, err = repository.Status(ctx)
	if err != nil || !status.Authorized || !status.PhoneCheck || status.Config.Environment != ProductionEnvironment || status.Config.TestDC != 0 {
		t.Fatal("production authorization not environment-bound", err)
	}
	for _, statement := range []string{
		"UPDATE account_config SET test_dc=2",
		"UPDATE account_config SET environment='test'",
		"UPDATE authentication_checks SET test_dc=2",
		"UPDATE authentication_checks SET environment='test'",
	} {
		if _, err := database.Exec(statement); err == nil {
			t.Fatal("mixed environment accepted")
		}
	}
	if err := current.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
}
