package main

import (
	"context"
	"io"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
)

// Go's Windows file handles support Close cancellation of pending pipe I/O.
func relayStdio(ctx context.Context, path string, input io.ReadCloser, output io.WriteCloser) error {
	return daemon.Relay(ctx, path, input, output)
}
