//go:build darwin || linux

package privatefs

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func createDirectory(path string) error { return os.Mkdir(path, 0700) }
func openPrivate(path string, directory, create bool) (*os.File, error) {
	flags := unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_RDONLY
	if directory {
		flags |= unix.O_DIRECTORY
	} else if create {
		flags = unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_RDWR | unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	kind, mode := uint32(unix.S_IFREG), os.FileMode(0600)
	if directory {
		kind = unix.S_IFDIR
		mode = 0700
	}
	if uint32(st.Mode)&unix.S_IFMT != kind || st.Uid != uint32(os.Geteuid()) || os.FileMode(st.Mode&07777) != mode || (!directory && st.Nlink != 1) {
		return nil, errors.Join(errors.New("private path has unsafe type, ownership, permissions, or links"), f.Close())
	}
	return f, nil
}
func createTemporary(dir string) (*os.File, error) { return os.CreateTemp(dir, ".private-*") }
func syncDirectory(dir string) error {
	f, err := openPrivate(dir, true, false)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func validatePlatformPath(string) error { return nil }
