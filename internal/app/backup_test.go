package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func sampleBackup() MetadataBackup {
	return MetadataBackup{Version: 1, Environment: "test", TestDC: 2, Retention: store.AuditRetention{Days: 30, MaxRecords: 10000}, Scopes: []policy.RecoverableScope{{Name: "work", Peers: []model.PeerID{}}}}
}

func TestBackupDecoderRejectsAmbiguousAndOversizedInput(t *testing.T) {
	data, _ := json.Marshal(sampleBackup())
	valid := string(data)
	cases := []string{"null", valid + valid, strings.Repeat(" ", MaximumBackupBytes+1), strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(valid, `"version"`, `"Version"`, 1), strings.Replace(valid, `"version":1`, `"version":2`, 1), strings.Replace(valid, `"days":30`, `"days":30,"days":30`, 1), strings.Replace(valid, `"days":30`, `"days":null`, 1), strings.Replace(valid, `"peers":[]`, `"peers":null`, 1), strings.Replace(valid, `"peers":[]`, `"peers":["tgpeer:v1:user:01"]`, 1), strings.Replace(valid, `"test_dc":2`, `"test_dc":2,"session":"hostile"`, 1), strings.Replace(valid, `"test_dc":2,`, "", 1), strings.Replace(valid, `"max_records":10000`, `"max_records":0`, 1)}
	for i, input := range cases {
		if _, err := decodeBackup([]byte(input)); !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("case %d accepted: %v", i, err)
		}
	}
	if _, err := decodeBackup(data); err != nil {
		t.Fatal(err)
	}
}

func TestBackupPrivateAtomicFileRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backup")
	if err := privatefs.EnsureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "backup.json")
	if err := writeBackup(path, sampleBackup()); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBackup(path); err != nil {
		t.Fatal(err)
	}
	if err := writeBackup(path, sampleBackup()); err == nil {
		t.Fatal("overwrote backup")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("existing backup changed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary files retained")
	}
	if runtime.GOOS == "windows" {
		return
	} // ACL and reparse-point rejection are covered by privatefs tests.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBackup(link); err == nil {
		t.Fatal("followed symlink")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBackup(path); err == nil {
		t.Fatal("read public backup")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeBackup(filepath.Join(dir, "new"), sampleBackup()); err == nil {
		t.Fatal("wrote public directory")
	}
}

func TestRecoveryRoundTripIsLocalAndRequiresStoppedDaemon(t *testing.T) {
	a := authorizedTextApplication(t, &fakeRuntime{})
	ctx := context.Background()
	a.factory = func(tgaccount.Config, *tgaccount.SessionStorage, tgaccount.Mode) (accountRuntime, error) {
		t.Fatal("opened Telegram")
		return nil, errors.New("unexpected")
	}
	member, _ := model.ParsePeerID("tgpeer:v1:channel:123")
	original, err := a.Scope(ctx, "", "work", []model.PeerID{member})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "backup")
	if err := privatefs.EnsureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "metadata.json")
	if err := a.SetFullRead(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := a.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, forbidden := range []string{"authorization", "api_id", "session", "grant", "full_read", "checkpoint", "request_id", original.ID.String()} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("exported excluded state", forbidden)
		}
	}
	lock, err := daemon.AcquireAccountLock(a.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Restore(ctx, path); !errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatal("restore allowed live daemon", err)
	}
	if _, err := a.AuditMaintenance(ctx, nil, false, false); !errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatal("maintenance allowed live daemon", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := a.Restore(ctx, path); err != nil {
		t.Fatal(err)
	}
	scopes, err := a.Scopes(ctx)
	if err != nil || len(scopes) != 1 || scopes[0].ID == original.ID {
		t.Fatal("scope restore", err)
	}
	full, err := a.FullRead(ctx)
	if err != nil || full {
		t.Fatal("restored full access", err)
	}
	b := sampleBackup()
	b.Environment = "production"
	b.TestDC = 0
	other := filepath.Join(dir, "other.json")
	if err := writeBackup(other, b); err != nil {
		t.Fatal(err)
	}
	if err := a.Restore(ctx, other); !errors.Is(err, ErrBackupEnvironment) {
		t.Fatal("cross-environment restore", err)
	}
}

func TestAuditMaintenanceWithoutAuthorization(t *testing.T) {
	a, _ := newTestApplication(t)
	ctx := context.Background()
	status, err := a.AuditMaintenance(ctx, nil, false, false)
	if err != nil || status.Retention.Days != 30 {
		t.Fatal(status, err)
	}
	r := store.AuditRetention{Days: 5, MaxRecords: 10}
	status, err = a.AuditMaintenance(ctx, &r, true, false)
	if err != nil || status.Retention != r {
		t.Fatal(status, err)
	}
	if _, err := a.AuditMaintenance(ctx, nil, false, true); err != nil {
		t.Fatal(err)
	}
}

func FuzzDecodeMetadataBackup(f *testing.F) {
	data, _ := json.Marshal(sampleBackup())
	f.Add(data)
	f.Add([]byte(`{"version":1,"version":2}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		backup, err := decodeBackup(data)
		if err != nil {
			return
		}
		if err := backup.validate(); err != nil {
			t.Fatal("accepted invalid backup")
		}
		encoded, err := json.Marshal(backup)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeBackup(encoded); err != nil {
			t.Fatal("valid backup cannot round trip", err)
		}
	})
}
