package keychaincheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeStore struct {
	value []byte
	calls int
	err   error
}

func (f *fakeStore) Get(context.Context, string) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.value == nil {
		return nil, keychain.ErrNotFound
	}
	return bytes.Clone(f.value), nil
}
func (f *fakeStore) Put(_ context.Context, _ string, value []byte) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.value = bytes.Clone(value)
	return nil
}
func (f *fakeStore) Delete(context.Context, string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	clear(f.value)
	f.value = nil
	return nil
}

func TestWorkerRejectsInputsBeforeOpeningStore(t *testing.T) {
	for _, args := range [][]string{nil, {"create"}, {"create", "default.session"}, {"read", strings.Repeat("a", 33)}, {"create", strings.ToUpper(testToken)}, {"set", testToken}, {"delete", testToken, "extra"}} {
		var output bytes.Buffer
		code := run(context.Background(), args, &output, func() (secretStore, error) { t.Fatal("invalid input opened store"); return nil, nil })
		if code != 1 || output.String() != "{\"ok\":false,\"category\":\"invalid_input\"}\n" {
			t.Fatalf("unexpected result %d %q", code, output.String())
		}
	}
}

func TestWorkerRoundTripAndExactValueComparison(t *testing.T) {
	store := &fakeStore{}
	open := func() (secretStore, error) { return store, nil }
	for _, action := range []string{"create", "read", "update", "verify", "delete", "absent"} {
		var output bytes.Buffer
		if code := run(context.Background(), []string{action, testToken}, &output, open); code != 0 {
			t.Fatalf("action=%s output=%s", action, output.String())
		}
	}
	store.value = []byte("a different value")
	var output bytes.Buffer
	if code := run(context.Background(), []string{"read", testToken}, &output, open); code != 1 || !strings.Contains(output.String(), "value_mismatch") {
		t.Fatal("different bytes accepted")
	}
	output.Reset()
	if code := run(context.Background(), []string{"create", testToken}, &output, open); code != 1 || !strings.Contains(output.String(), "already_exists") {
		t.Fatal("existing item overwritten")
	}
	if string(store.value) != "a different value" {
		t.Fatal("collision changed bytes")
	}
}

func TestWorkerErrorsAreFixedAndCancellationAvoidsStore(t *testing.T) {
	var output bytes.Buffer
	store := &fakeStore{err: errors.New("private upstream details")}
	if code := run(context.Background(), []string{"read", testToken}, &output, func() (secretStore, error) { return store, nil }); code != 1 || strings.Contains(output.String(), "private") {
		t.Fatal("raw error escaped")
	}
	var result Result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Category != "native_failure" {
		t.Fatal("invalid failure result")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output.Reset()
	run(ctx, []string{"create", testToken}, &output, func() (secretStore, error) { t.Fatal("cancelled worker opened store"); return nil, nil })
	if !strings.Contains(output.String(), "cancelled") {
		t.Fatal("cancellation lost")
	}
}
