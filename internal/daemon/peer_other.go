//go:build !darwin && !linux && !windows

package daemon

import "net"

// VerifyPeer fails closed on platforms without a supported peer check.
func VerifyPeer(net.Conn) error { return ErrUnsafeSocket }
