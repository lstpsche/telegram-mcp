package privatefs

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestRejectForeignACL(t *testing.T) {
	dir := privateDirectory(t)
	path := filepath.Join(dir, "state")
	if err := WriteFile(path, []byte("synthetic"), false); err != nil {
		t.Fatal(err)
	}
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;;FA;;;" + sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path, 100); err == nil {
		t.Fatal("accepted world-readable file")
	}
	if err := WriteFile(path, nil, true); err == nil {
		t.Fatal("replaced unsafe file")
	}
}

func TestRejectReparsePoints(t *testing.T) {
	dir := privateDirectory(t)
	target := filepath.Join(dir, "state")
	if err := WriteFile(target, []byte("synthetic"), false); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("symlink privilege or Developer Mode is unavailable")
		}
		t.Fatal(err)
	}
	if _, err := ReadFile(link, 100); err == nil {
		t.Fatal("accepted reparse point")
	}
	if err := WriteFile(link, nil, true); err == nil {
		t.Fatal("replaced reparse point")
	}
}

func TestRejectNetworkPathsAndStreams(t *testing.T) {
	for _, path := range []string{`\\server\share\state`, `C:\state:stream`, `C:\state.`, `C:\state `} {
		if err := CheckFile(path); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
		if err := validPath(path); err == nil {
			t.Fatalf("accepted unsafe path syntax %q", path)
		}
	}
}

func TestRejectAncestorWithForeignDeleteRights(t *testing.T) {
	parent := privateDirectory(t)
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid.String() + ")(A;;0x00000040;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirectory(filepath.Join(parent, "child")); err == nil {
		t.Fatal("accepted ancestor with foreign delete-child rights")
	}
}
