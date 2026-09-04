package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
	fileDescriptor, err := unix.Open(path, unix.O_CLOEXEC|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open account lock: %w", err)
	}
	file := os.NewFile(uintptr(fileDescriptor), path)
	if file == nil {
		_ = unix.Close(fileDescriptor)
		return nil, errors.New("open account lock: invalid file descriptor")
	}
	closeOnError := func(openError error) (*AccountLock, error) {
		if closeError := file.Close(); closeError != nil {
			return nil, errors.Join(openError, fmt.Errorf("close account lock: %w", closeError))
		}
		return nil, openError
	}

	var status unix.Stat_t
	if err := unix.Fstat(fileDescriptor, &status); err != nil {
		return closeOnError(fmt.Errorf("inspect account lock: %w", err))
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG {
		return closeOnError(errors.New("account lock must be a regular file"))
	}
	if status.Uid != uint32(os.Geteuid()) {
		return closeOnError(errors.New("account lock must be owned by the current user"))
	}
	if permissions := os.FileMode(status.Mode).Perm(); permissions != 0o600 {
		return closeOnError(fmt.Errorf("account lock permissions are %04o, require 0600", permissions))
	}
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
