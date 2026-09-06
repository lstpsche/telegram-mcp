package daemon

import (
	"errors"
	"golang.org/x/sys/unix"
	"net"
	"os"
)

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
		peer, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil || peer.Uid != uint32(os.Geteuid()) {
			peerError = errors.Join(ErrUnsafeSocket, err)
		}
	})
	if err != nil {
		return errors.Join(ErrUnsafeSocket, err)
	}
	return peerError
}
