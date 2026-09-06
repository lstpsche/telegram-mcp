package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/buildinfo"
	operatorcli "github.com/lstpsche/telegram-mcp/internal/cli"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/keychaincheck"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/secrets"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
	metastore "github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

type controller interface {
	Configure(context.Context, string, int, app.ConfigurationReader) error
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
	if handled, code := dispatchControl(ctx, os.Args[1:]); handled {
		os.Exit(code)
	}
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

	if args[0] == "access" && len(args) > 1 && args[1] == "setup" {
		return runAccessSetupCommand(ctx, args, stdout, stderr)
	}
	if args[0] == "upgrade" {
		return runUpgradeCommand(ctx, args, stdout, stderr)
	}
	if args[0] == "install" {
		return runInstallCommand(ctx, args, stdout, stderr)
	}
	if args[0] == "setup" {
		return runSetupCommand(ctx, args, stdout, stderr)
	}

	if isSupportCommand(args[0]) {
		return runSupportCommand(ctx, args, stdout, stderr, defaultSupport)
	}

	if args[0] == "backup-inspect" {
		return runMaintenanceCommand(ctx, args, stdout, stderr, nil)
	}
	control, err := newController()
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	switch args[0] {
	case "migrate-keychain":
		if len(args) != 2 || args[1] != "--accept-plaintext-storage" {
			fmt.Fprintln(stderr, "telegram-mcpctl: migrate-keychain requires --accept-plaintext-storage")
			return 2
		}
		migration, ok := control.(interface{ MigrateKeychain(context.Context) error })
		if !ok {
			fmt.Fprintln(stderr, "telegram-mcpctl: legacy migration is unavailable")
			return 1
		}
		if err := migration.MigrateKeychain(ctx); err != nil {
			writeControlError(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "Credentials and sessions copied into private unencrypted local storage; legacy Keychain items were retained.")
		return 0
	case "backup", "restore", "audit":
		return runMaintenanceCommand(ctx, args, stdout, stderr, control)
	case "access":
		return runAccessCommand(ctx, args, stdout, stderr, control)
	case "scopes", "scope", "unscope":
		return runScopeCommand(ctx, args, stdout, stderr, control)
	case "peers", "saved-message", "grants", "grant", "revoke":
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
		fmt.Fprintln(stdout, "Telegram session removed and authorization epoch invalidated.")
		return 0
	case "configure":
		environment, testDC, ok := parseConfiguration(args[1:])
		if !ok {
			fmt.Fprintln(stderr, "telegram-mcpctl: configure requires --test-dc {1|2|3} or --production --attest-eligible")
			return 2
		}
		if err := control.Configure(ctx, environment, testDC, configurationReader(openTerminal)); err != nil {
			writeControlError(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Telegram %s account configured; authenticate interactively before granting content access.\n", environment)
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
			fmt.Fprintf(stdout, "Telegram %s authentication passed; a new authorization epoch is active.\n", method)
		} else {
			fmt.Fprintln(stdout, "The existing Telegram session is authorized; no new method check was recorded.")
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

func parseConfiguration(args []string) (string, int, bool) {
	if len(args) != 2 {
		return "", 0, false
	}
	if args[0] == "--test-dc" {
		dc, err := strconv.Atoi(args[1])
		if err == nil && args[1] == strconv.Itoa(dc) && dc >= 1 && dc <= 3 {
			return tgaccount.TestEnvironment, dc, true
		}
	}
	if (args[0] == "--production" && args[1] == "--attest-eligible") || (args[1] == "--production" && args[0] == "--attest-eligible") {
		return tgaccount.ProductionEnvironment, 0, true
	}
	return "", 0, false
}

func writeHelp(writer io.Writer) {
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  telegram-mcpctl setup")
	fmt.Fprintln(writer, "  telegram-mcpctl install --version X.Y.Z [--setup]")
	fmt.Fprintln(writer, "  telegram-mcpctl upgrade --version X.Y.Z")
	fmt.Fprintln(writer, "  telegram-mcpctl migrate-keychain --accept-plaintext-storage")
	fmt.Fprintln(writer, "Metadata maintenance requires a stopped daemon; backup files require an absolute path in a private 0700 directory.")
	fmt.Fprintln(writer, "  telegram-mcpctl backup --file ABSOLUTE_FILE")
	fmt.Fprintln(writer, "  telegram-mcpctl backup-inspect --file ABSOLUTE_FILE")
	fmt.Fprintln(writer, "  telegram-mcpctl restore --file ABSOLUTE_FILE --replace-scopes --reset-access")
	fmt.Fprintln(writer, "  telegram-mcpctl audit")
	fmt.Fprintln(writer, "  telegram-mcpctl audit retention --days N --max-records N --apply")
	fmt.Fprintln(writer, "  telegram-mcpctl audit prune --confirm")
	fmt.Fprintln(writer, "  telegram-mcpctl audit purge --all --confirm")
	fmt.Fprintln(writer, "  telegram-mcpctl configure --test-dc {1|2|3}")
	fmt.Fprintln(writer, "  telegram-mcpctl configure --production --attest-eligible")
	fmt.Fprintln(writer, "  telegram-mcpctl auth {phone|qr}")
	fmt.Fprintln(writer, "  telegram-mcpctl service install --bin-dir ABSOLUTE_DIRECTORY")
	fmt.Fprintln(writer, "  telegram-mcpctl service {start|stop|restart|uninstall}")
	fmt.Fprintln(writer, "  telegram-mcpctl doctor")
	fmt.Fprintln(writer, "  telegram-mcpctl agent-config")
	fmt.Fprintln(writer, "  telegram-mcpctl status")
	fmt.Fprintln(writer, "  telegram-mcpctl logout")
	fmt.Fprintln(writer, "  telegram-mcpctl peers")
	fmt.Fprintln(writer, "  telegram-mcpctl saved-message")
	fmt.Fprintln(writer, "  telegram-mcpctl access")
	fmt.Fprintln(writer, "  telegram-mcpctl access setup")
	fmt.Fprintln(writer, "  telegram-mcpctl access full --accept-full-read")
	fmt.Fprintln(writer, "  telegram-mcpctl access restricted")
	fmt.Fprintln(writer, "Full read access includes supported conversations, all supported message authors and history, images, and read acknowledgments until revoked.")
	fmt.Fprintln(writer, "Content is disclosed to the connected agent and its model provider. Enabling access does not establish permission under Telegram terms.")
	fmt.Fprintln(writer, "  telegram-mcpctl grants")
	fmt.Fprintln(writer, "  telegram-mcpctl grant --peer PEER --author AUTHOR --min-id N --max-id N --read-through N --expires-at RFC3339 --profile {self-authored|consented} --attest-eligible [--allow-images]")
	fmt.Fprintln(writer, "  telegram-mcpctl revoke --peer PEER")
	fmt.Fprintln(writer, "  telegram-mcpctl scopes")
	fmt.Fprintln(writer, "  telegram-mcpctl scope --name NAME [--id ID] [--peer PEER ...]")
	fmt.Fprintln(writer, "  telegram-mcpctl unscope --id ID")
	fmt.Fprintln(writer, "Authentication is interactive through the OS console; content access requires human grants or explicit Full read access.")
}

func writeStatus(writer io.Writer, status app.Status) {
	fmt.Fprintf(writer, "daemon=%s\n", status.Daemon)
	fmt.Fprintf(writer, "configured=%t\n", status.Configured)
	if status.Configured {
		fmt.Fprintf(writer, "environment=%s\n", status.Environment)
		if status.Environment == tgaccount.TestEnvironment {
			fmt.Fprintf(writer, "test_dc=%d\n", status.TestDC)
		}
	}
	fmt.Fprintf(writer, "authorization_recorded=%t\n", status.Authorized)
	fmt.Fprintf(writer, "phone_check=%t\n", status.PhoneCheckPassed)
	fmt.Fprintf(writer, "qr_check=%t\n", status.QRCheckPassed)
	fmt.Fprintln(writer, "production_login=supported")
}

func writeControlError(writer io.Writer, err error) {
	if writeHumanHint(writer, err) {
		return
	}
	switch {
	case errors.Is(err, app.ErrMigrationUnavailable):
		fmt.Fprintln(writer, "telegram-mcpctl: migration requires a cgo-enabled macOS control binary signed with the existing Keychain identity; see docs/keychain.md")
	case errors.Is(err, app.ErrLocalSecretsUnavailable):
		fmt.Fprintln(writer, "telegram-mcpctl: configured local credentials are unavailable; migrate an existing Keychain installation or recover the account explicitly before reconfiguration")
	case errors.Is(err, secrets.ErrStoreExists):
		fmt.Fprintln(writer, "telegram-mcpctl: local secret storage already exists; migration never replaces or merges it")
	case errors.Is(err, secrets.ErrInvalidStore):
		fmt.Fprintln(writer, "telegram-mcpctl: local secret storage is invalid; no secret details were emitted")
	case errors.Is(err, app.ErrInvalidBackup):
		fmt.Fprintln(writer, "telegram-mcpctl: invalid metadata backup; expected supported version, exact fields and bounded scopes/settings")
	case errors.Is(err, app.ErrBackupFile):
		fmt.Fprintln(writer, "telegram-mcpctl: backup file failed validation or I/O; require an owner-only directory and regular file (Unix 0700/0600 or restricted Windows ACLs), no links or overwrite")
	case errors.Is(err, app.ErrBackupEnvironment):
		fmt.Fprintln(writer, "telegram-mcpctl: backup environment differs from the configured account; configure and authenticate the intended environment separately")
	case errors.Is(err, metastore.ErrInvalidRetention):
		fmt.Fprintln(writer, "telegram-mcpctl: audit retention requires 1–3650 days and 1–1000000 records")
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
		fmt.Fprintln(writer, "telegram-mcpctl: account runtime is busy; stop the daemon before authentication changes, peer discovery or metadata maintenance")
	case errors.Is(err, metastore.ErrAuthorizationExists):
		fmt.Fprintln(writer, "telegram-mcpctl: log out before changing Telegram application credentials")
	case errors.Is(err, app.ErrConfigurationRequired):
		fmt.Fprintln(writer, "telegram-mcpctl: run configure before authentication or logout")
	case errors.Is(err, tgaccount.ErrInvalidConfig):
		fmt.Fprintln(writer, "telegram-mcpctl: invalid Telegram application credentials")
	case errors.Is(err, tgaccount.ErrPasswordRequired):
		fmt.Fprintln(writer, "telegram-mcpctl: Telegram requested your existing Two-Step Verification password; an empty value cannot skip it")
	case errors.Is(err, tgaccount.ErrAuthenticationRateLimited):
		fmt.Fprintln(writer, "telegram-mcpctl: Telegram is rate-limiting authentication; pause login attempts before trying again")
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
