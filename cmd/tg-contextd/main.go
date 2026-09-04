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
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, buildinfo.String("tg-contextd"))
		return 0
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "usage: tg-contextd")
		fmt.Fprintln(stdout, "Runs the single-account Telegram Test-DC daemon; production login is disabled in Phase 1.")
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(stderr, "tg-contextd: no arguments are accepted")
		return 2
	}
	application, err := newDaemon()
	if err != nil {
		writeDaemonError(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "tg-contextd: starting owner-only Telegram Test-DC runtime")
	if err := application.RunDaemon(ctx); err != nil {
		writeDaemonError(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "tg-contextd: stopped cleanly")
	return 0
}

func defaultDaemon() (daemonApplication, error) {
	return app.NewDefault()
}

func writeDaemonError(writer io.Writer, err error) {
	switch {
	case errors.Is(err, app.ErrConfigurationRequired):
		fmt.Fprintln(writer, "tg-contextd: Test-DC configuration is required; run tg-contextctl configure")
	case errors.Is(err, daemon.ErrAccountLocked):
		fmt.Fprintln(writer, "tg-contextd: another account runtime already owns the lock")
	case errors.Is(err, daemon.ErrUnsafeSocket), errors.Is(err, daemon.ErrSocketInUse):
		fmt.Fprintln(writer, "tg-contextd: runtime socket validation failed closed")
	case errors.Is(err, keychain.ErrKeychainLocked), errors.Is(err, keychain.ErrWrongKeychain), errors.Is(err, keychain.ErrUnsupported):
		fmt.Fprintln(writer, "tg-contextd: the unlocked macOS login keychain is required")
	case errors.Is(err, telegramruntime.ErrReauthenticationRequired):
		fmt.Fprintln(writer, "tg-contextd: Telegram authorization expired; reauthenticate before restarting")
	case errors.Is(err, telegramruntime.ErrTelegramUnavailable):
		fmt.Fprintln(writer, "tg-contextd: Telegram connection is unavailable")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(writer, "tg-contextd: shutdown cancelled")
	default:
		fmt.Fprintln(writer, "tg-contextd: runtime failed safely; no Telegram details were emitted")
	}
}
