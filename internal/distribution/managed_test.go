package distribution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func TestStableEntriesSurviveVersionChange(t *testing.T) {
	i, archive := releaseFixture(t)
	old, err := i.installArchive(context.Background(), "0.2.0", archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareEntries(i.Root, old); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(i.Root, Binary("telegram-mcp"))
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(i.Root, "versions", "0.3.0")
	if err := privatefs.EnsureDirectory(next); err != nil {
		t.Fatal(err)
	}
	if err := privatefs.WriteExecutable(filepath.Join(next, Binary("telegram-mcp")), []byte("new executable")); err != nil {
		t.Fatal(err)
	}
	if err := prepareEntries(i.Root, next); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("stable relay replaced")
	}
	data, err := privatefs.ReadExecutable(path, 64)
	if err != nil || string(data) != "synthetic executable" {
		t.Fatal("relay bytes changed", err)
	}
	if ManagedVersion(i.Root, next) != "0.3.0" || ManagedVersion(i.Root, filepath.Join(i.Root, "unmanaged", "0.3.0")) != "" {
		t.Fatal("incorrect layout recognition")
	}
}
