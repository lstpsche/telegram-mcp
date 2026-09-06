package cli

import "golang.org/x/sys/unix"

const terminalGetAttributes = unix.TIOCGETA
const terminalSetAttributes = unix.TIOCSETA
