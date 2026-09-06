package control

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type migrationController struct {
	controller
	called bool
	err    error
}

func (m *migrationController) MigrateKeychain(context.Context) error { m.called = true; return m.err }

func TestMigrationRequiresExplicitPlaintextAcceptance(t *testing.T) {
	for _, args := range [][]string{{"migrate-keychain"}, {"migrate-keychain", "--accept-plaintext-storage", "extra"}} {
		var out, stderr bytes.Buffer
		control := &migrationController{}
		code := runContext(context.Background(), args, &out, &stderr, func() (controller, error) { return control, nil }, nil)
		if code != 2 || control.called || out.Len() != 0 {
			t.Fatal("migration ran without explicit valid acceptance")
		}
	}
}
func TestMigrationOutputDoesNotExposeUnderlyingSecretError(t *testing.T) {
	const sentinel = "synthetic-sensitive-error"
	var out, stderr bytes.Buffer
	control := &migrationController{err: errors.New(sentinel)}
	code := runContext(context.Background(), []string{"migrate-keychain", "--accept-plaintext-storage"}, &out, &stderr, func() (controller, error) { return control, nil }, nil)
	if code != 1 || !control.called || out.Len() != 0 || strings.Contains(stderr.String(), sentinel) {
		t.Fatal("migration error boundary failed")
	}
}
