package privatefs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func privateDirectory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private")
	if err := EnsureDirectory(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrivateFilePublication(t *testing.T) {
	dir := privateDirectory(t)
	path := filepath.Join(dir, "state")
	if err := WriteFile(path, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("second"), false); err == nil {
		t.Fatal("exclusive publication overwrote a file")
	}
	data, err := ReadFile(path, 5)
	if err != nil || !bytes.Equal(data, []byte("first")) {
		t.Fatalf("read: %q, %v", data, err)
	}
	if data, err := ReadFile(path, 4); err == nil || data != nil {
		t.Fatal("size limit returned bytes")
	}
	if err := WriteFile(path, []byte("second"), true); err != nil {
		t.Fatal(err)
	}
	data, err = ReadFile(path, 6)
	if err != nil || string(data) != "second" {
		t.Fatalf("replacement: %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leaked: %v, %v", entries, err)
	}
	if err := CheckFile(path); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateFilesRejectHardLinks(t *testing.T) {
	dir := privateDirectory(t)
	path := filepath.Join(dir, "state")
	if err := WriteFile(path, []byte("synthetic"), false); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(path, 100); err == nil || data != nil {
		t.Fatal("accepted hardlinked input")
	}
	if err := WriteFile(path, []byte("changed"), true); err == nil {
		t.Fatal("replaced hardlinked input")
	}
}

func TestPrivateFilesRequireBoundedPathsAndInputs(t *testing.T) {
	if err := EnsureDirectory("relative"); err == nil {
		t.Fatal("accepted relative directory")
	}
	dir := privateDirectory(t)
	if _, err := ReadFile(filepath.Join(dir, "absent"), 100); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if err := CheckFile(dir); err == nil {
		t.Fatal("accepted directory as file")
	}
	for _, limit := range []int64{0, -1, int64(^uint64(0) >> 1)} {
		if _, err := ReadFile(filepath.Join(dir, "absent"), limit); err == nil {
			t.Fatal("accepted invalid limit")
		}
	}
}

func TestRejectNoncanonicalPath(t *testing.T) {
	dir := privateDirectory(t)
	path := dir + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(dir)
	if err := EnsureDirectory(path); err == nil {
		t.Fatal("accepted noncanonical directory")
	}
}
