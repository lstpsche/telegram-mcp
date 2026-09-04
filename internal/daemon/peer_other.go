//go:build !darwin

package daemon

import "net"

// VerifyPeer fails closed on platforms without a supported peer check.
func VerifyPeer(*net.UnixConn) error { return ErrUnsafeSocket }
