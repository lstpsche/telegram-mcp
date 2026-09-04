package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const lockHelperEnvironment = "TGCONTEXT_TEST_LOCK_PATH"

func TestAccountLockIsExclusiveAndReusable(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "state", lockFilename)
	first, err := AcquireAccountLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AcquireAccountLock(lockPath)
	if second != nil || !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("second AcquireAccountLock() = %v, %v", second, err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
	reopened, err := AcquireAccountLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAccountLockRejectsUnsafeNodes(t *testing.T) {
	t.Parallel()

	t.Run("symlink", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(stateDir, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(stateDir, lockFilename)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if lock, err := AcquireAccountLock(link); err == nil {
			_ = lock.Release()
			t.Fatal("AcquireAccountLock() accepted a symlink")
		}
	})

	t.Run("wrong permissions", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(stateDir, lockFilename)
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lockPath, 0o640); err != nil {
			t.Fatal(err)
		}
		if lock, err := AcquireAccountLock(lockPath); err == nil {
			_ = lock.Release()
			t.Fatal("AcquireAccountLock() accepted unsafe permissions")
		}
	})
}

func TestAccountLockExcludesAnotherProcess(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state", lockFilename)
	processContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(processContext, os.Args[0], "-test.run=^TestAccountLockHelper$")
	command.Env = []string{lockHelperEnvironment + "=" + lockPath}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("lock helper did not become ready: %q, %v", scanner.Text(), scanner.Err())
	}
	if lock, err := AcquireAccountLock(lockPath); lock != nil || !errors.Is(err, ErrAccountLocked) {
		_ = stdin.Close()
		_ = command.Wait()
		t.Fatalf("AcquireAccountLock() against child = %v, %v", lock, err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestAccountLockHelper(t *testing.T) {
	lockPath := os.Getenv(lockHelperEnvironment)
	if lockPath == "" {
		return
	}
	lock, err := AcquireAccountLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}
