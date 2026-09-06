package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/buildinfo"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/keychaincheck"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
	telegramruntime "github.com/lstpsche/telegram-mcp/internal/telegram"
)

type daemonApplication interface {
	RunDaemon(context.Context) error
}

type daemonFactory func() (daemonApplication, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr, defaultDaemon))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr, defaultDaemon)
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer, newDaemon daemonFactory) int {
	if len(args) > 0 && args[0] == "--keychain-probe" {
		return keychaincheck.Run(ctx, args[1:], stdout)
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, buildinfo.String("telegram-mcpd"))
		return 0
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "usage: telegram-mcpd")
		fmt.Fprintln(stdout, "Runs the single-account Telegram daemon; configure and authenticate through telegram-mcp.")
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(stderr, "telegram-mcpd: no arguments are accepted")
		return 2
	}
	application, err := newDaemon()
	if err != nil {
		writeDaemonError(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "telegram-mcpd: starting owner-only Telegram Test-DC runtime")
	if err := application.RunDaemon(ctx); err != nil {
		writeDaemonError(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "telegram-mcpd: stopped cleanly")
	return 0
}

func defaultDaemon() (daemonApplication, error) {
	return app.NewDefault()
}

func writeDaemonError(writer io.Writer, err error) {
	switch {
	case errors.Is(err, app.ErrConfigurationRequired):
		fmt.Fprintln(writer, "telegram-mcpd: Test-DC configuration is required; run telegram-mcp configure")
	case errors.Is(err, daemon.ErrAccountLocked):
		fmt.Fprintln(writer, "telegram-mcpd: another account runtime already owns the lock")
	case errors.Is(err, daemon.ErrUnsafeSocket), errors.Is(err, daemon.ErrSocketInUse):
		fmt.Fprintln(writer, "telegram-mcpd: runtime socket validation failed closed")
	case errors.Is(err, keychain.ErrKeychainLocked), errors.Is(err, keychain.ErrWrongKeychain), errors.Is(err, keychain.ErrUnsupported):
		fmt.Fprintln(writer, "telegram-mcpd: the unlocked macOS login keychain is required")
	case errors.Is(err, telegramruntime.ErrReauthenticationRequired):
		fmt.Fprintln(writer, "telegram-mcpd: Telegram authorization expired; reauthenticate before restarting")
	case errors.Is(err, telegramruntime.ErrTelegramUnavailable):
		fmt.Fprintln(writer, "telegram-mcpd: Telegram connection is unavailable")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(writer, "telegram-mcpd: shutdown cancelled")
	default:
		fmt.Fprintln(writer, "telegram-mcpd: runtime failed safely; no Telegram details were emitted")
	}
}
