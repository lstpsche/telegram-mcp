package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"golang.org/x/sys/windows"
)

var ErrAccountLocked = errors.New("the account runtime is already locked")

type AccountLock struct {
	file *os.File
	once sync.Once
	err  error
}

func AcquireAccountLock(path string) (*AccountLock, error) {
	if err := privatefs.EnsureDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := privatefs.OpenFile(path, true)
	if err != nil {
		return nil, err
	}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			err = ErrAccountLocked
		}
		return nil, errors.Join(err, f.Close())
	}
	return &AccountLock{file: f}, nil
}
func (l *AccountLock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file != nil {
			l.err = errors.Join(windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &windows.Overlapped{}), l.file.Close())
		}
	})
	return l.err
}
