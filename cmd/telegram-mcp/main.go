package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lstpsche/telegram-mcp/internal/control"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/logging"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runContext(ctx context.Context, args []string, input io.ReadCloser, output io.WriteCloser, diagnostics io.Writer) int {
	if handled, code := runEntryInfo(args, output, diagnostics); handled {
		return code
	}
	if len(args) != 0 {
		return control.Run(ctx, args, output, diagnostics)
	}
	logger := logging.New(diagnostics, nil)
	deadline, err := startupDeadline()
	if err == nil {
		if handled, code := dispatchManagedEntry(ctx, input, output, diagnostics, deadline); handled {
			return code
		}
	}
	paths, pathError := daemon.DefaultPaths()
	if err == nil {
		err = pathError
	}
	if err == nil {
		startup, cancel := context.WithDeadline(ctx, deadline)
		connection, connectError := connectDaemon(startup, paths)
		cancel()
		err = connectError
		if err == nil {
			err = relayStdio(ctx, connection, input, output)
			// Covers stdio preparation failures before the bridge takes ownership.
			_ = connection.Close()
		}
	}
	if err != nil {
		category := model.ErrorNotReady
		if ctx.Err() != nil {
			category = model.ErrorCancelled
		}
		logger.Error(ctx, logging.EventOperationFailed, logging.ComponentField(logging.ComponentRelay), logging.ErrorCategoryField(category), logging.StartupReasonField(startupReason(err)))
		return 1
	}
	return 0
}
