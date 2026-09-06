package privatefs

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Directory-specific access masks from WinNT.h.
const (
	fileAddFile         = 0x0002
	fileAddSubdirectory = 0x0004
	fileDeleteChild     = 0x0040
)

func checkAncestors(path string) error {
	user, err := CurrentUserSID()
	if err != nil {
		return err
	}
	nearest := true
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		err := inspectWindowsAncestor(parent, user, nearest)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			nearest = false
		}
		if filepath.Dir(parent) == parent {
			return nil
		}
	}
}

func inspectWindowsAncestor(path string, user *windows.SID, nearest bool) (result error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(handle)) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("private path ancestor is not an ordinary directory")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !trustedAncestorSID(owner, user) {
		return errors.New("private path ancestor has an untrusted owner")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return errors.New("private path ancestor has unrestricted access")
	}
	mask := windows.ACCESS_MASK(windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | fileDeleteChild)
	if nearest {
		mask |= fileAddFile | fileAddSubdirectory
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("private path ancestor has an unsupported ACL")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Mask&mask != 0 && !trustedAncestorSID(sid, user) {
			return errors.New("private path ancestor permits another user to replace local state")
		}
	}
	return nil
}

func trustedAncestorSID(sid, user *windows.SID) bool {
	if sid.Equals(user) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return true
	}
	// Windows Resource Protection owns OS directories through TrustedInstaller.
	return sid.String() == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
}
