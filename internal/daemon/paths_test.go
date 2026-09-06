package daemon

import (
	"path/filepath"
	"testing"
)

func TestNewPathsRequiresBoundedAbsoluteRoots(t *testing.T) {
	t.Parallel()

	if _, err := NewPaths("relative", t.TempDir()); err == nil {
		t.Fatal("NewPaths() accepted a relative state directory")
	}
	if _, err := NewPaths(string(filepath.Separator), t.TempDir()); err == nil {
		t.Fatal("NewPaths() accepted the filesystem root")
	}
	paths, err := NewPaths(filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(paths.Database) != databaseFilename || filepath.Base(paths.Socket) != socketFilename {
		t.Fatalf("NewPaths() = %#v", paths)
	}
}
