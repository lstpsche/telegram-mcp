package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

func TestOfflineLeaseUsesPrivateXDGStorageWithoutAServiceHome(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "/")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	if _, err := service.Default(); err == nil {
		t.Fatal("test home unexpectedly qualifies for per-user service management")
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(paths.StateDir, "service.lock")
	called := false
	if err := defaultOfflineLease(context.Background(), func(context.Context) error {
		called = true
		if lock, err := daemon.AcquireAccountLock(leasePath); lock != nil || !errors.Is(err, daemon.ErrAccountLocked) {
			t.Fatalf("offline command did not hold the shared lifecycle lock: %v", err)
		}
		account, err := daemon.AcquireAccountLock(paths.Lock)
		if err != nil {
			return err
		}
		return account.Release()
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("offline operation was skipped")
	}
	lock, err := daemon.AcquireAccountLock(leasePath)
	if err != nil {
		t.Fatal("offline command retained the lifecycle lock", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
