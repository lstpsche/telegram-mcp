package keychain

import (
	"context"
	"errors"
	"testing"
)

func TestNewRejectsUnsafeServiceNames(t *testing.T) {
	t.Parallel()

	for _, service := range []string{"", "contains spaces", "line\nbreak", "-leading"} {
		if _, err := New(service); err == nil {
			t.Fatalf("New(%q) accepted unsafe service", service)
		}
	}
	if _, err := New("dev.telegram-mcp.session"); err != nil {
		t.Fatalf("New(valid) error = %v", err)
	}
}

func TestCancelledContextDoesNotTouchKeychain(t *testing.T) {
	t.Parallel()

	store, err := New("dev.telegram-mcp.cancelled-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, "session", []byte("not-written")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v, want context.Canceled", err)
	}
	if _, err := store.Get(ctx, "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
	if err := store.Delete(ctx, "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete() error = %v, want context.Canceled", err)
	}
}

func TestCallValidationDoesNotTouchKeychain(t *testing.T) {
	t.Parallel()

	var zero Store
	if err := zero.Put(context.Background(), "session", []byte("value")); err == nil {
		t.Fatal("zero Store accepted a write")
	}
	store, err := New("dev.telegram-mcp.validation-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "contains spaces", []byte("value")); err == nil {
		t.Fatal("Put() accepted an unsafe account")
	}
	if err := store.Put(context.Background(), "session", nil); err == nil {
		t.Fatal("Put() accepted an empty secret")
	}
}
