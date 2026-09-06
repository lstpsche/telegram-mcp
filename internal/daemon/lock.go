//go:build darwin || linux

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"golang.org/x/sys/unix"
)

var ErrAccountLocked = errors.New("the account runtime is already locked")

// AccountLock holds the single-account advisory lock for its whole lifetime.
type AccountLock struct {
	file *os.File
	once sync.Once
	err  error
}

// AcquireAccountLock opens a no-follow, owner-only regular file and takes an
// exclusive non-blocking advisory lock.
func AcquireAccountLock(path string) (*AccountLock, error) {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := privatefs.OpenFile(path, true)
	if err != nil {
		return nil, fmt.Errorf("open account lock: %w", err)
	}
	fileDescriptor := int(file.Fd())
	closeOnError := func(openError error) (*AccountLock, error) { return nil, errors.Join(openError, file.Close()) }
	if err := unix.Flock(fileDescriptor, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return closeOnError(ErrAccountLocked)
		}
		return closeOnError(fmt.Errorf("acquire account lock: %w", err))
	}
	return &AccountLock{file: file}, nil
}

// Release unlocks and closes the lock. It is safe to call more than once.
func (l *AccountLock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file == nil {
			return
		}
		unlockError := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
		closeError := l.file.Close()
		if unlockError != nil {
			l.err = fmt.Errorf("release account lock: %w", unlockError)
		}
		if closeError != nil {
			l.err = errors.Join(l.err, fmt.Errorf("close account lock: %w", closeError))
		}
	})
	return l.err
}
