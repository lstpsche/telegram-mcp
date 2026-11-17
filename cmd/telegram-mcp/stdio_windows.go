package main

import (
	"context"
	"io"
	"net"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
)

// Go's Windows file handles support Close cancellation of pending pipe I/O.
func relayStdio(ctx context.Context, connection net.Conn, input io.ReadCloser, output io.WriteCloser) error {
	return daemon.RelayConnection(ctx, connection, input, output)
}
