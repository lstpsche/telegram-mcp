package privatefs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
