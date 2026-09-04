package daemon

import (
	"testing"
	"time"
)

func TestLifecycleAcceptsOperationalTransitions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	lifecycle, err := NewLifecycle(func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []State{StateLocked, StateConnecting, StateReady, StateStopping, StateStopped} {
		if err := lifecycle.Transition(next); err != nil {
			t.Fatalf("Transition(%q) error = %v", next, err)
		}
	}
	if got := lifecycle.Snapshot(); got.State != StateStopped || got.ChangedAt.IsZero() {
		t.Fatalf("Snapshot() = %#v", got)
	}
	if err := lifecycle.Transition(StateStarting); err == nil {
		t.Fatal("terminal lifecycle accepted another transition")
	}
}

func TestLifecycleAllowsReauthorizationAndFailure(t *testing.T) {
	t.Parallel()

	lifecycle, err := NewLifecycle(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []State{StateLocked, StateConnecting, StateReauthRequired, StateConnecting, StateFailed} {
		if err := lifecycle.Transition(next); err != nil {
			t.Fatalf("Transition(%q) error = %v", next, err)
		}
	}
}
