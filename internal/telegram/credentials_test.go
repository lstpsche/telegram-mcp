package telegram

import (
	"context"
	"errors"
	"testing"
)

type credentialMemory struct {
	value   []byte
	failure error
}

func (m *credentialMemory) Put(_ context.Context, _ string, value []byte) error {
	if m.failure != nil {
		return m.failure
	}
	m.value = append([]byte(nil), value...)
	return nil
}
func (m *credentialMemory) Get(context.Context, string) ([]byte, error) {
	return append([]byte(nil), m.value...), m.failure
}
func (*credentialMemory) Delete(context.Context, string) error { return nil }

func TestCredentialsRoundTripAndRejectCorruption(t *testing.T) {
	ctx := context.Background()
	memory := &credentialMemory{}
	config := Config{APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef"), TestDC: 2}
	if err := StoreCredentials(ctx, memory, config); err != nil {
		t.Fatal(err)
	}
	saved := append([]byte(nil), memory.value...)
	read, err := LoadCredentials(ctx, memory)
	if err != nil || read.APIID != config.APIID || read.TestDC != config.TestDC || string(read.APIHash) != string(config.APIHash) {
		t.Fatal("credential tuple did not round trip")
	}
	clear(read.APIHash)
	for _, bad := range []string{`null`, `{"version":9}`, `{"version":1,"api_id":12345,"test_dc":2,"api_hash":"invalid"}`, `{"version":1,"private-field":"must-not-escape"}`} {
		memory.value = []byte(bad)
		if value, err := LoadCredentials(ctx, memory); !errors.Is(err, ErrInvalidConfig) || len(value.APIHash) != 0 {
			t.Fatalf("corrupt credentials were not sanitized: %v", err)
		}
	}
	memory.value = saved
	memory.failure = errors.New("write failed")
	if err := StoreCredentials(ctx, memory, Config{APIID: 54321, APIHash: config.APIHash, TestDC: 3}); err == nil {
		t.Fatal("write failure ignored")
	}
	memory.failure = nil
	read, err = LoadCredentials(ctx, memory)
	if err != nil || read.APIID != 12345 || read.TestDC != 2 {
		t.Fatal("failed atomic write changed credential tuple")
	}
	clear(read.APIHash)
}
