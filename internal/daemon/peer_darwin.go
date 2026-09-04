//go:build darwin

package daemon

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
)

// VerifyPeer requires the socket peer to belong to this OS user.
func VerifyPeer(connection *net.UnixConn) error {
	raw, err := connection.SyscallConn()
	if err != nil {
		return ErrUnsafeSocket
	}
	var peerError error
	err = raw.Control(func(fd uintptr) {
		peer, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil || peer.Uid != uint32(os.Geteuid()) {
			peerError = ErrUnsafeSocket
		}
	})
	if err != nil {
		return ErrUnsafeSocket
	}
	return peerError
}
