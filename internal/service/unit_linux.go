package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Only vendor paths reach this reader. Resolve a distribution's /lib alias,
// then require root-owned ancestors and a non-writable regular config file.
func readVendorDropIn(path string) (data []byte, err error) {
	directory, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if err := inspectAncestors(directory, 0); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(directory, filepath.Base(path)), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			data = nil
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o022 != 0 || stat.Uid != 0 || stat.Size > maxInstallationBytes {
		return nil, ErrInvalidInstallation
	}
	data, err = io.ReadAll(io.LimitReader(f, maxInstallationBytes+1))
	if len(data) > maxInstallationBytes {
		return nil, ErrInvalidInstallation
	}
	return data, err
}

func readUnit(path string) (data []byte, err error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			data = nil
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, ErrInvalidInstallation
	}
	data, err = io.ReadAll(io.LimitReader(f, maxInstallationBytes+1))
	if len(data) > maxInstallationBytes {
		return nil, ErrInvalidInstallation
	}
	return data, err
}
