package daemon

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type State string

const (
	StateStarting       State = "starting"
	StateLocked         State = "locked"
	StateConnecting     State = "connecting"
	StateReady          State = "ready"
	StateReauthRequired State = "reauth_required"
	StateStopping       State = "stopping"
	StateStopped        State = "stopped"
	StateFailed         State = "failed"
)

type Snapshot struct {
	State     State
	ChangedAt time.Time
}

// Lifecycle validates and records the daemon's process-local state machine.
type Lifecycle struct {
	mutex    sync.RWMutex
	snapshot Snapshot
	now      func() time.Time
}

func NewLifecycle(now func() time.Time) (*Lifecycle, error) {
	if now == nil {
		return nil, errors.New("lifecycle clock is required")
	}
	changedAt := now()
	if changedAt.IsZero() {
		return nil, errors.New("lifecycle clock returned zero time")
	}
	return &Lifecycle{
		snapshot: Snapshot{State: StateStarting, ChangedAt: changedAt.UTC()},
		now:      now,
	}, nil
}

func (l *Lifecycle) Transition(next State) error {
	if l == nil {
		return errors.New("lifecycle is not initialized")
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if !validTransition(l.snapshot.State, next) {
		return fmt.Errorf("invalid daemon lifecycle transition %s -> %s", l.snapshot.State, next)
	}
	changedAt := l.now()
	if changedAt.IsZero() {
		return errors.New("lifecycle clock returned zero time")
	}
	l.snapshot = Snapshot{State: next, ChangedAt: changedAt.UTC()}
	return nil
}

func (l *Lifecycle) Snapshot() Snapshot {
	if l == nil {
		return Snapshot{}
	}
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	return l.snapshot
}

func validTransition(current, next State) bool {
	switch current {
	case StateStarting:
		return next == StateLocked || next == StateStopping || next == StateFailed
	case StateLocked:
		return next == StateConnecting || next == StateReauthRequired || next == StateStopping || next == StateFailed
	case StateConnecting:
		return next == StateReady || next == StateReauthRequired || next == StateStopping || next == StateFailed
	case StateReady:
		return next == StateReauthRequired || next == StateStopping || next == StateFailed
	case StateReauthRequired:
		return next == StateConnecting || next == StateStopping || next == StateFailed
	case StateStopping:
		return next == StateStopped || next == StateFailed
	default:
		return false
	}
}
