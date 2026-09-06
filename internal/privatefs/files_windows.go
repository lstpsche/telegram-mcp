package privatefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CurrentUserSID identifies the interactive account rather than a group token.
func CurrentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

// SecurityDescriptor grants only the current user access, without inherited ACEs.
func SecurityDescriptor() (string, error) {
	sid, err := CurrentUserSID()
	if err != nil {
		return "", err
	}
	return "O:" + sid.String() + "D:P(A;OICI;FA;;;" + sid.String() + ")", nil
}

func attributes() (*windows.SecurityAttributes, error) {
	sddl, err := SecurityDescriptor()
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}, nil
}

func createDirectory(path string) error {
	attrs, err := attributes()
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.CreateDirectory(name, attrs)
}

func openPrivate(path string, directory, create bool) (*os.File, error) {
	disposition := uint32(windows.OPEN_EXISTING)
	access := uint32(windows.GENERIC_READ | windows.READ_CONTROL)
	if create {
		disposition = windows.OPEN_ALWAYS
		access |= windows.GENERIC_WRITE
	}
	return openChecked(path, directory, disposition, access)
}

func openChecked(path string, directory bool, disposition, access uint32) (*os.File, error) {
	attrs, err := attributes()
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, attrs, disposition, flags, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(handle), path)
	if err := checkHandle(handle, directory); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func checkHandle(handle windows.Handle, directory bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || (!directory && info.NumberOfLinks != 1) {
		return errors.New("private path has unsafe type or links")
	}
	return checkHandleOwner(handle)
}

// checkHandleOwner rejects null DACLs and any ACE granting another identity access.
func checkHandleOwner(handle windows.Handle) error {
	sid, err := CurrentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(sid) {
		return errors.New("private object is not owned by the current user")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil || acl.AceCount == 0 {
		return errors.New("private object requires an owner-only ACL")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(sid) {
			return errors.New("private object ACL grants another identity access")
		}
	}
	return nil
}

func createTemporary(dir string) (*os.File, error) {
	for range 10 {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, ".private-"+hex.EncodeToString(token[:]))
		f, err := openChecked(name, false, windows.CREATE_NEW, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		return f, err
	}
	return nil, errors.New("cannot allocate private temporary file")
}
func publish(source, target string, replace bool) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(from, to, flags)
}

// Windows does not expose directory fsync; publication requests MOVEFILE_WRITE_THROUGH.
func syncDirectory(string) error { return nil }

// Reject network/device namespaces and alternate streams for on-machine state.
func validatePlatformPath(path string) error {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || strings.Contains(path[len(volume):], ":") {
		return errors.New("private path must use a local drive and an ordinary file")
	}
	for _, component := range strings.Split(filepath.Clean(path)[len(volume):], string(filepath.Separator)) {
		if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return errors.New("private path has an ambiguous Windows component")
		}
	}
	return nil
}

func openExecutable(path string) (*os.File, error) { return openPrivate(path, false, false) }
