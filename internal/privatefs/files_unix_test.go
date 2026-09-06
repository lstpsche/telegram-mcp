//go:build darwin || linux

package privatefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectUnsafePermissionsAndSymlinks(t *testing.T) {
	dir := privateDirectory(t)
	path := filepath.Join(dir, "state")
	if err := WriteFile(path, []byte("synthetic"), false); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(link, 100); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := WriteFile(link, nil, true); err == nil {
		t.Fatal("replaced symlink")
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path, 100); err == nil {
		t.Fatal("accepted group-readable file")
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirectory(dir); err == nil {
		t.Fatal("accepted group-accessible directory")
	}
}

func TestRejectWritableAncestors(t *testing.T) {
	parent := privateDirectory(t)
	child := filepath.Join(parent, "child")
	if err := EnsureDirectory(child); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(child, "state")
	if err := WriteFile(path, []byte("synthetic"), false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path, 100); err == nil {
		t.Fatal("accepted attacker-writable ancestor")
	}
	if err := EnsureDirectory(filepath.Join(parent, "new")); err == nil {
		t.Fatal("created below attacker-writable ancestor")
	}
	if err := os.Chmod(parent, os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path, 100); err != nil {
		t.Fatalf("trusted sticky ancestor: %v", err)
	}
}
