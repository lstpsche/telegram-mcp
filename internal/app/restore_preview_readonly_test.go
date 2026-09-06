package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func TestRestorePreviewDoesNotInitializeAbsentMetadata(t *testing.T) {
	application, _ := newTestApplication(t)
	backupDir := filepath.Join(t.TempDir(), "backup")
	if err := privatefs.EnsureDirectory(backupDir); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(backupDir, "settings.json")
	if err := writeBackup(backupPath, sampleBackup()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(application.paths.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture database already exists: %v", err)
	}
	if _, err := application.PreviewRestore(context.Background(), backupPath); err == nil {
		t.Fatal("preview accepted missing account metadata")
	}
	if _, err := os.Stat(application.paths.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only preview created account metadata: %v", err)
	}
}
