package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lstpsche/telegram-mcp/internal/buildinfo"
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
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(output, buildinfo.String("telegram-mcp"))
		return 0
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(output, "usage: telegram-mcp [--version]")
		fmt.Fprintln(output, "Connect standard MCP stdio to the running local daemon.")
		return 0
	}
	logger := logging.New(diagnostics, nil)
	if len(args) != 0 {
		logger.Error(ctx, logging.EventOperationFailed, logging.ComponentField(logging.ComponentRelay), logging.ErrorCategoryField(model.ErrorInvalidInput))
		return 2
	}
	paths, err := daemon.DefaultPaths()
	if err == nil {
		err = relayStdio(ctx, paths.Socket, input, output)
	}
	if err != nil {
		category := model.ErrorNotReady
		if ctx.Err() != nil {
			category = model.ErrorCancelled
		}
		logger.Error(ctx, logging.EventOperationFailed, logging.ComponentField(logging.ComponentRelay), logging.ErrorCategoryField(category))
		return 1
	}
	return 0
}
