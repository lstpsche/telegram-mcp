package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func fileFixture(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestFileStorePersistsAcrossRestartWithoutUnlock(t *testing.T) {
	ctx := context.Background()
	s := fileFixture(t)
	if _, err := s.Get(ctx, "default.credentials"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	credentials := []byte("synthetic credentials")
	if err := s.Put(ctx, "default.credentials", credentials); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "production.session", []byte("synthetic session")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "default.cursor-integrity", bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	second, err := NewFileStore(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	value, err := second.Get(ctx, "default.credentials")
	if err != nil || !bytes.Equal(value, credentials) {
		t.Fatal("restart changed credentials", err)
	}
	clear(value)
	if err := second.Delete(ctx, "production.session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "production.session"); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted session available", err)
	}
	if err := privatefs.CheckFile(filepath.Join(s.directory, "secrets.json")); err != nil {
		t.Fatal(err)
	}
}
func TestFileStoreFailsOnCorruptionAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	s := fileFixture(t)
	if err := s.Put(ctx, "default.credentials", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.directory, "secrets.json")
	for _, data := range [][]byte{[]byte("broken"), []byte(`{"version":1,"version":1}`), []byte(`{"version":1,"unknown":"sensitive"}`)} {
		if err := privatefs.WriteFile(path, data, true); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, "production.session", []byte("replacement")); !errors.Is(err, ErrInvalidStore) {
			t.Fatal("corruption substituted", err)
		}
		after, err := privatefs.ReadFile(path, maximumFileBytes)
		if err != nil || !bytes.Equal(after, data) {
			t.Fatal("corruption overwritten", err)
		}
	}
}
func TestFileStoreImportIsAtomicAndNeverReplaces(t *testing.T) {
	ctx := context.Background()
	s := fileFixture(t)
	values := map[string][]byte{"default.credentials": []byte("synthetic credentials"), "production.session": []byte("synthetic session")}
	if err := s.Import(ctx, values); err != nil {
		t.Fatal(err)
	}
	if err := s.Import(ctx, values); !errors.Is(err, ErrStoreExists) {
		t.Fatal("import replaced initialized store", err)
	}
	original, _ := s.Get(ctx, "production.session")
	if !bytes.Equal(original, values["production.session"]) {
		t.Fatal("import lost item")
	}
	clear(original)
	other := fileFixture(t)
	values["default.cursor-integrity"] = []byte("bad")
	if err := other.Import(ctx, values); !errors.Is(err, ErrInvalidStore) {
		t.Fatal("invalid migration accepted", err)
	}
	if _, err := os.Lstat(filepath.Join(other.directory, "secrets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed import published file")
	}
}
func TestMissingStoreCannotBeRecreatedBySessionWrite(t *testing.T) {
	ctx := context.Background()
	s := fileFixture(t)
	if err := s.Put(ctx, "production.session", []byte("synthetic")); !errors.Is(err, ErrNotFound) {
		t.Fatal("session created unconfigured store", err)
	}
	if err := s.Put(ctx, "../escape", []byte("synthetic")); err == nil {
		t.Fatal("unknown slot accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Put(canceled, "default.credentials", []byte("synthetic")); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled write accepted", err)
	}
}
