package cli

import "golang.org/x/sys/unix"

const terminalGetAttributes = unix.TCGETS
const terminalSetAttributes = unix.TCSETS
