package privatefs

import "golang.org/x/sys/unix"

func publish(source, target string, replace bool) error {
	var flags uint32
	if !replace {
		flags = unix.RENAME_EXCL
	}
	return unix.RenamexNp(source, target, flags)
}
