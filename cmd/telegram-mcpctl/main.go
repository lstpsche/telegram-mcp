package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/buildinfo"
	operatorcli "github.com/lstpsche/telegram-mcp/internal/cli"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/keychaincheck"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
	metastore "github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

type controller interface {
	Configure(context.Context, int, app.ConfigurationReader) error
	Authenticate(context.Context, tgaccount.AuthMethod, tgaccount.Prompt) (app.AuthOutcome, error)
	Logout(context.Context) error
	Status(context.Context) (app.Status, error)
}

type terminal interface {
	tgaccount.Prompt
	ReadAPIID(context.Context) (int, error)
	ReadAPIHash(context.Context) ([]byte, error)
	Close() error
}

type controllerFactory func() (controller, error)
type terminalFactory func() (terminal, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr, defaultController, defaultTerminal))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr, defaultController, defaultTerminal)
}

func runContext(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	newController controllerFactory,
	openTerminal terminalFactory,
) int {
	if len(args) > 0 && args[0] == "--keychain-probe" {
		return keychaincheck.Run(ctx, args[1:], stdout)
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, buildinfo.String("telegram-mcpctl"))
		return 0
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		writeHelp(stdout)
		return 0
	}

	if isSupportCommand(args[0]) {
		return runSupportCommand(ctx, args, stdout, stderr, defaultSupport)
	}

	control, err := newController()
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	switch args[0] {
	case "scopes", "scope", "unscope":
		return runScopeCommand(ctx, args, stdout, stderr, control)
	case "peers", "grants", "grant", "revoke":
		return runTextCommand(ctx, args, stdout, stderr, control)
	case "status":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "telegram-mcpctl: status accepts no arguments")
			return 2
		}
		status, err := control.Status(ctx)
		if err != nil {
			writeControlError(stderr, err)
			return 1
		}
		writeStatus(stdout, status)
		return 0
	case "logout":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "telegram-mcpctl: logout accepts no arguments")
			return 2
		}
		if err := control.Logout(ctx); err != nil {
			writeControlError(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "Telegram Test-DC session removed and authorization epoch invalidated.")
		return 0
	case "configure":
		testDC, ok := parseTestDC(args[1:])
		if !ok {
			fmt.Fprintln(stderr, "telegram-mcpctl: configure requires exactly --test-dc 1, 2, or 3")
			return 2
		}
		if err := control.Configure(ctx, testDC, configurationReader(openTerminal)); err != nil {
			writeControlError(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Telegram Test DC %d configured; production login remains disabled.\n", testDC)
		return 0
	case "auth":
		if len(args) != 2 || (args[1] != "phone" && args[1] != "qr") {
			fmt.Fprintln(stderr, "telegram-mcpctl: auth requires exactly one method: phone or qr")
			return 2
		}
		prompt, err := openTerminal()
		if err != nil {
			writeControlError(stderr, err)
			return 1
		}
		defer prompt.Close()
		method := tgaccount.AuthMethodPhone
		if args[1] == "qr" {
			method = tgaccount.AuthMethodQR
		}
		outcome, err := control.Authenticate(ctx, method, prompt)
		if err != nil {
			writeControlError(stderr, err)
			return 1
		}
		if outcome.Performed {
			fmt.Fprintf(stdout, "Telegram Test-DC %s authentication passed; a new authorization epoch is active.\n", method)
		} else {
			fmt.Fprintln(stdout, "The existing Telegram Test-DC session is authorized; no new method check was recorded.")
		}
		return 0
	default:
		fmt.Fprintln(stderr, "telegram-mcpctl: unknown command")
		return 2
	}
}

func configurationReader(openTerminal terminalFactory) app.ConfigurationReader {
	return func(ctx context.Context) (int, []byte, error) {
		prompt, err := openTerminal()
		if err != nil {
			return 0, nil, err
		}
		defer prompt.Close()
		apiID, err := prompt.ReadAPIID(ctx)
		if err != nil {
			return 0, nil, err
		}
		apiHash, err := prompt.ReadAPIHash(ctx)
		if err != nil {
			clear(apiHash)
			return 0, nil, err
		}
		return apiID, apiHash, nil
	}
}

func defaultController() (controller, error) {
	return app.NewDefault()
}

func defaultTerminal() (terminal, error) {
	return operatorcli.OpenTerminal()
}

func parseTestDC(args []string) (int, bool) {
	flags := flag.NewFlagSet("configure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	testDC := flags.Int("test-dc", 0, "Telegram Test DC number")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *testDC < 1 || *testDC > 3 {
		return 0, false
	}
	return *testDC, true
}

func writeHelp(writer io.Writer) {
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  telegram-mcpctl configure --test-dc {1|2|3}")
	fmt.Fprintln(writer, "  telegram-mcpctl auth {phone|qr}")
	fmt.Fprintln(writer, "  telegram-mcpctl service install --bin-dir ABSOLUTE_DIRECTORY")
	fmt.Fprintln(writer, "  telegram-mcpctl service {start|stop|restart|uninstall}")
	fmt.Fprintln(writer, "  telegram-mcpctl doctor")
	fmt.Fprintln(writer, "  telegram-mcpctl agent-config")
	fmt.Fprintln(writer, "  telegram-mcpctl status")
	fmt.Fprintln(writer, "  telegram-mcpctl logout")
	fmt.Fprintln(writer, "  telegram-mcpctl peers")
	fmt.Fprintln(writer, "  telegram-mcpctl grants")
	fmt.Fprintln(writer, "  telegram-mcpctl grant --peer PEER --author AUTHOR --min-id N --max-id N --read-through N --expires-at RFC3339 --profile {self-authored|consented} --attest-eligible [--allow-images]")
	fmt.Fprintln(writer, "  telegram-mcpctl revoke --peer PEER")
	fmt.Fprintln(writer, "  telegram-mcpctl scopes")
	fmt.Fprintln(writer, "  telegram-mcpctl scope --name NAME [--id ID] [--peer PEER ...]")
	fmt.Fprintln(writer, "  telegram-mcpctl unscope --id ID")
	fmt.Fprintln(writer, "Authentication is interactive through /dev/tty; production login is disabled.")
}

func writeStatus(writer io.Writer, status app.Status) {
	fmt.Fprintf(writer, "daemon=%s\n", status.Daemon)
	fmt.Fprintf(writer, "configured=%t\n", status.Configured)
	if status.Configured {
		fmt.Fprintln(writer, "environment=test")
		fmt.Fprintf(writer, "test_dc=%d\n", status.TestDC)
	}
	fmt.Fprintf(writer, "authorization_recorded=%t\n", status.Authorized)
	fmt.Fprintf(writer, "test_dc_phone_check=%t\n", status.PhoneCheckPassed)
	fmt.Fprintf(writer, "test_dc_qr_check=%t\n", status.QRCheckPassed)
	fmt.Fprintln(writer, "production_login=disabled")
}

func writeControlError(writer io.Writer, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(writer, "telegram-mcpctl: operation cancelled")
	case errors.Is(err, policy.ErrBusy):
		fmt.Fprintln(writer, "telegram-mcpctl: text policy is busy; retry after the in-flight content request or grant operation finishes")
	case errors.Is(err, policy.ErrInvalidGrant):
		fmt.Fprintln(writer, "telegram-mcpctl: invalid text grant; check IDs, scope, eligibility, and expiry within 30 days")
	case errors.Is(err, policy.ErrInvalidScope):
		fmt.Fprintln(writer, "telegram-mcpctl: invalid named scope; check its name, ID, unique supported peers, and scope limits")
	case errors.Is(err, model.ErrInvalidReference):
		fmt.Fprintln(writer, "telegram-mcpctl: invalid or unavailable reference; use a current exact identifier")
	case errors.Is(err, policy.ErrEpochChanged):
		fmt.Fprintln(writer, "telegram-mcpctl: authorization epoch is unavailable or changed; authenticate and retry")
	case errors.Is(err, app.ErrTextControlUnsupported):
		fmt.Fprintln(writer, "telegram-mcpctl: account runtime does not support text access control")
	case errors.Is(err, daemon.ErrAccountLocked):
		fmt.Fprintln(writer, "telegram-mcpctl: account runtime is busy; stop the daemon before authentication changes or peer discovery")
	case errors.Is(err, metastore.ErrAuthorizationExists):
		fmt.Fprintln(writer, "telegram-mcpctl: log out before changing Test-DC application credentials")
	case errors.Is(err, app.ErrConfigurationRequired):
		fmt.Fprintln(writer, "telegram-mcpctl: run configure before authentication or logout")
	case errors.Is(err, tgaccount.ErrInvalidConfig):
		fmt.Fprintln(writer, "telegram-mcpctl: invalid Test-DC application credentials")
	case errors.Is(err, tgaccount.ErrAuthenticationRejected):
		fmt.Fprintln(writer, "telegram-mcpctl: Telegram rejected the interactive authentication input")
	case errors.Is(err, tgaccount.ErrReauthenticationRequired):
		fmt.Fprintln(writer, "telegram-mcpctl: the Telegram session requires reauthentication")
	case errors.Is(err, tgaccount.ErrTelegramUnavailable):
		fmt.Fprintln(writer, "telegram-mcpctl: Telegram operation is unavailable; retry later")
	case errors.Is(err, keychain.ErrKeychainLocked), errors.Is(err, keychain.ErrWrongKeychain), errors.Is(err, keychain.ErrUnsupported):
		fmt.Fprintln(writer, "telegram-mcpctl: the unlocked macOS login keychain is required")
	case errors.Is(err, daemon.ErrUnsafeSocket), errors.Is(err, daemon.ErrSocketInUse):
		fmt.Fprintln(writer, "telegram-mcpctl: runtime socket validation failed closed")
	default:
		fmt.Fprintln(writer, "telegram-mcpctl: operation failed safely; no credential details were emitted")
	}
}
