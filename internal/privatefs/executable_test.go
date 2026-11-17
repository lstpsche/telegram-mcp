package privatefs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExecutableReplacementRequiresTheInspectedPrivateFile(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "private")
	if err := EnsureDirectory(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "entry")
	if err := WriteExecutable(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplaceExecutable(path, []byte("new"), before); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(path)
	if err != nil || os.SameFile(before, after) {
		t.Fatal("replacement did not publish a new file", err)
	}
	if err := CheckExecutable(path); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceExecutable(path, []byte("stale"), before); err == nil {
		t.Fatal("accepted a stale inspected file")
	}
	if err := WriteExecutable(path, []byte("overwrite")); !errors.Is(err, os.ErrExist) {
		t.Fatal("ordinary executable publication replaced the entry", err)
	}
	data, err := ReadExecutable(path, 64)
	if err != nil || string(data) != "new" {
		t.Fatal("failed replacement changed the entry", err)
	}
	if err := ReplaceExecutable(path, []byte("missing identity"), nil); err == nil {
		t.Fatal("accepted replacement without inspected identity")
	}
}

func TestExecutableReadDoesNotRelaxPrivateDataMode(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "private")
	if err := EnsureDirectory(root); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "program")
	if err := WriteFile(name, []byte("executable"), false); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if _, err := ReadExecutable(name, 64); err == nil {
			t.Fatal("accepted non-executable file")
		}
		if err := os.Chmod(name, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFile(name, 64); err == nil {
			t.Fatal("relaxed private data permissions")
		}
	}
	if data, err := ReadExecutable(name, 64); err != nil || string(data) != "executable" {
		t.Fatal(err)
	}
	if _, err := ReadExecutable(name, 2); err == nil {
		t.Fatal("ignored size bound")
	}
}
