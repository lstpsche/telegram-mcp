//go:build darwin

package daemon

import (
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// VerifyPeer requires the socket peer to belong to this OS user.
func VerifyPeer(connection net.Conn) error {
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return ErrUnsafeSocket
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	var peerError error
	err = raw.Control(func(fd uintptr) {
		peer, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil || peer.Uid != uint32(os.Geteuid()) {
			peerError = errors.Join(ErrUnsafeSocket, err)
		}
	})
	if err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	return peerError
}
