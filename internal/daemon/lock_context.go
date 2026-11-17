package daemon

import (
	"context"
	"errors"
	"time"
)

// AcquireAccountLockContext waits for ownership without opening account data.
// Callers bound the wait; ordinary offline operations retain nonblocking locks.
func AcquireAccountLockContext(ctx context.Context, path string) (*AccountLock, error) {
	var contention error
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(contention, err)
		}
		lock, err := AcquireAccountLock(path)
		if err == nil {
			if err := ctx.Err(); err != nil {
				return nil, errors.Join(err, lock.Release())
			}
			return lock, nil
		}
		if !errors.Is(err, ErrAccountLocked) {
			return nil, err
		}
		contention = err
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}
