package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

func runUpgradeCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 || args[1] != "--version" || !distribution.ValidVersion(args[2]) {
		fmt.Fprintln(stderr, "telegram-mcpctl: use upgrade --version X.Y.Z")
		return 2
	}
	if err := upgradeDefault(ctx, args[2], stdout); err != nil {
		fmt.Fprintln(stderr, "telegram-mcpctl: upgrade failed; run doctor and inspect service registration. Downloaded versions and account data were retained; no downgrade was attempted.")
		writeSupportError(stderr, err)
		return 1
	}
	return 0
}

func upgradeDefault(ctx context.Context, version string, output io.Writer) (result error) {
	installer, err := distribution.Default()
	if err != nil {
		return err
	}
	if err := privatefs.EnsureDirectory(installer.Root); err != nil {
		return err
	}
	lock, err := daemon.AcquireAccountLock(filepath.Join(installer.Root, "upgrade.lock"))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Release()) }()
	local, err := defaultSupport()
	if err != nil {
		return err
	}
	installed, err := local.service.Inspect(ctx)
	if err != nil {
		return err
	}
	if installed != nil {
		previous := distribution.ManagedVersion(installer.Root, installed.BinDir)
		if previous != "" {
			order, err := distribution.CompareVersions(version, previous)
			if err != nil {
				return err
			}
			if order < 0 {
				return errors.New("downgrades are not supported")
			}
		}
	}
	directory, err := installer.Install(ctx, version)
	if err != nil {
		return err
	}
	if err := verifyReleasePrograms(ctx, directory, version, runClientCommand); err != nil {
		return err
	}
	if err := activateRelease(ctx, local, installed, directory); err != nil {
		return err
	}
	relay, err := distribution.Relay(directory, "")
	if err != nil {
		return err
	}
	if err := writeTextJSON(output, struct{ Version, Relay, Control string }{version, relay, filepath.Join(installer.Root, distribution.Binary("telegram-mcpctl"))}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Upgrade complete: the service responds. Reconnect your MCP client; the managed relay path is unchanged. Account readiness and content permission are separate checks.")
	return err
}

func activateRelease(ctx context.Context, local support, installed *service.Config, directory string) error {
	// A known absent service is a valid starting state, not an ignored failure.
	if installed != nil && installed.BinDir != directory {
		if err := local.service.Stop(ctx); err != nil && !errors.Is(err, service.ErrServiceAbsent) {
			return err
		}
		if err := local.service.Uninstall(ctx); err != nil {
			return err
		}
		installed = nil
	}
	if installed == nil {
		if _, err := local.service.Install(ctx, directory); err != nil {
			return err
		}
	}
	if err := local.service.Start(ctx); err != nil && !errors.Is(err, service.ErrServiceLoaded) {
		return err
	}
	_, err := waitForRuntime(ctx, local.inspect, false)
	return err
}

func verifyReleasePrograms(ctx context.Context, directory, version string, run func(context.Context, string, ...string) ([]byte, error)) error {
	var commit string
	for _, name := range []string{"telegram-mcp", "telegram-mcpctl", "telegram-mcpd"} {
		output, err := run(ctx, filepath.Join(directory, distribution.Binary(name)), "--version")
		if err != nil {
			return err
		}
		prefix := "Telegram MCP " + name + " version=" + version + " commit="
		line := strings.TrimSuffix(string(output), "\n")
		if !strings.HasPrefix(line, prefix) {
			return errors.New("release executable identity mismatch")
		}
		revision := strings.TrimPrefix(line, prefix)
		if len(revision) != 40 || strings.Trim(revision, "0123456789abcdef") != "" {
			return errors.New("invalid release executable revision")
		}
		if commit != "" && revision != commit {
			return errors.New("mixed release executables")
		}
		commit = revision
	}
	return nil
}

// Only the stable human control entry delegates. The relay never dispatches,
// starts services, authenticates, reconnects or replays an MCP request.
func dispatchControl(ctx context.Context, args []string) (bool, int) {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "telegram-mcpctl: cannot resolve control executable")
		return true, 1
	}
	root, err := distribution.DefaultRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "telegram-mcpctl: cannot resolve installation directory")
		return true, 1
	}
	if executable != filepath.Join(root, distribution.Binary("telegram-mcpctl")) {
		return false, 0
	}
	if len(args) == 0 || args[0] == "install" || args[0] == "--help" || args[0] == "-h" {
		return false, 0
	}
	if err := privatefs.CheckExecutable(executable); err != nil {
		writeSupportError(os.Stderr, err)
		return true, 1
	}
	local, err := defaultSupport()
	if err != nil {
		writeSupportError(os.Stderr, err)
		return true, 1
	}
	installed, err := local.service.Inspect(ctx)
	if err != nil {
		writeSupportError(os.Stderr, err)
		return true, 1
	}
	if installed == nil {
		if args[0] == "upgrade" || args[0] == "doctor" || args[0] == "agent-config" || args[0] == "--version" {
			return false, 0
		}
		fmt.Fprintln(os.Stderr, "telegram-mcpctl: no active version; run install --version X.Y.Z --setup")
		return true, 1
	}
	if distribution.ManagedVersion(root, installed.BinDir) == "" {
		fmt.Fprintln(os.Stderr, "telegram-mcpctl: existing service is unmanaged; use upgrade --version X.Y.Z to adopt it")
		if args[0] == "upgrade" {
			return false, 0
		}
		return true, 1
	}
	command := exec.CommandContext(ctx, installed.Control, args...)
	command.WaitDelay = 5 * time.Second
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return true, exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "telegram-mcpctl: active control program failed to start")
		return true, 1
	}
	return true, 0
}
