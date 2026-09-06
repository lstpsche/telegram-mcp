package privatefs

import "golang.org/x/sys/unix"

func publish(source, target string, replace bool) error {
	var flags uint
	if !replace {
		flags = unix.RENAME_NOREPLACE
	}
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, flags)
}
