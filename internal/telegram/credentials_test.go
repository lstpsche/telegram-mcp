package telegram

import (
	"context"
	"encoding/json"
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
	config := Config{Environment: TestEnvironment, APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef"), TestDC: 2}
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
	if err := StoreCredentials(ctx, memory, Config{Environment: TestEnvironment, APIID: 54321, APIHash: config.APIHash, TestDC: 3}); err == nil {
		t.Fatal("write failure ignored")
	}
	memory.failure = nil
	read, err = LoadCredentials(ctx, memory)
	if err != nil || read.APIID != 12345 || read.TestDC != 2 {
		t.Fatal("failed atomic write changed credential tuple")
	}
	clear(read.APIHash)
}

func TestCredentialsRequireExplicitEnvironmentExceptLegacyTestVersion(t *testing.T) {
	ctx := context.Background()
	memory := &credentialMemory{}
	for _, config := range []Config{
		{Environment: TestEnvironment, TestDC: 2, APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef")},
		{Environment: ProductionEnvironment, APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef")},
	} {
		if err := StoreCredentials(ctx, memory, config); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadCredentials(ctx, memory)
		clear(loaded.APIHash)
		if err != nil || loaded.Environment != config.Environment || loaded.TestDC != config.TestDC {
			t.Fatal("wrong credential environment", err)
		}
	}
	hash := []byte("0123456789abcdef0123456789abcdef")
	value, err := json.Marshal(credentials{Version: 1, APIID: 12345, APIHash: hash, TestDC: 2})
	if err != nil {
		t.Fatal(err)
	}
	memory.value = value
	loaded, err := LoadCredentials(ctx, memory)
	clear(loaded.APIHash)
	if err != nil || loaded.Environment != TestEnvironment || loaded.TestDC != 2 {
		t.Fatal("legacy test credentials lost", err)
	}
	for _, stored := range []credentials{
		{Version: 1, APIID: 12345, APIHash: hash, TestDC: 0},
		{Version: 1, Environment: ProductionEnvironment, APIID: 12345, APIHash: hash},
		{Version: 2, APIID: 12345, APIHash: hash, TestDC: 2},
		{Version: 2, Environment: ProductionEnvironment, APIID: 12345, APIHash: hash, TestDC: 2},
		{Version: 2, Environment: "unknown", APIID: 12345, APIHash: hash},
	} {
		memory.value, err = json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadCredentials(ctx, memory)
		if !errors.Is(err, ErrInvalidConfig) || len(loaded.APIHash) != 0 {
			t.Fatal("ambiguous credential environment accepted")
		}
	}
}
