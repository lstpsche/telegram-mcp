package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountOwnershipWaitIsBoundedAndCancellable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", lockFilename)
	owner, err := AcquireAccountLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	lock, err := AcquireAccountLockContext(ctx, path)
	if lock != nil || !errors.Is(err, ErrAccountLocked) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended ownership wait: lock=%v error=%v", lock, err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	lock, err = AcquireAccountLockContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	absent := filepath.Join(filepath.Dir(path), "absent")
	if lock, err := AcquireAccountLockContext(cancelled, absent); lock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ownership wait: lock=%v error=%v", lock, err)
	}
	if _, err := os.Lstat(absent); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled wait created a lock file", err)
	}
}
