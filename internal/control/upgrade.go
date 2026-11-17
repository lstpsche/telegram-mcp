package control

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
		fmt.Fprintln(stderr, "telegram-mcp: use upgrade --version X.Y.Z")
		return 2
	}
	if err := upgradeDefault(ctx, args[2], stdout); err != nil {
		fmt.Fprintln(stderr, "telegram-mcp: upgrade failed; run doctor and inspect service registration. Downloaded versions and account data were retained; no downgrade was attempted.")
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
				return humanHint("downgrades are not supported")
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
	if err := distribution.EnsureEntry(ctx, directory); err != nil {
		return err
	}
	enabled, err := activateUpgrade(ctx, local, directory)
	if err != nil {
		return err
	}
	relay, err := distribution.VerifiedRelay(ctx, directory, "")
	if err != nil {
		return err
	}
	if err := writeTextJSON(output, struct{ Version, Relay, Control string }{version, relay, filepath.Join(installer.Root, distribution.Binary("telegram-mcp"))}); err != nil {
		return err
	}
	if !enabled {
		_, err = fmt.Fprintln(output, "Upgrade complete: the new version is installed and the service remains stopped. Run service start to resume it.")
		return err
	}
	_, err = fmt.Fprintln(output, "Upgrade complete: the service responds. Reconnect your MCP client; the managed relay path is unchanged. Account readiness and content permission are separate checks.")
	return err
}

// Missing registration cannot retain the previous human startup choice. An
// explicit upgrade can rebuild it disabled; only a later human start resumes it.
func activateUpgrade(ctx context.Context, local support, directory string) (enabled bool, result error) {
	result = local.withSession(ctx, func(manager lifecycleService) error {
		current, err := manager.Inspect(ctx)
		if err != nil {
			return err
		}
		if err := checkManagedSwitch(current, directory); err != nil {
			return err
		}
		if current != nil {
			enabled, err = manager.Enabled(ctx)
			absent := errors.Is(err, service.ErrServiceAbsent)
			if err != nil && !absent {
				return err
			}
			if absent {
				enabled = false
			}
			if current.BinDir != directory && !absent {
				if err := manager.Quiesce(ctx); err != nil {
					return err
				}
			}
		}
		return activateStoppedRelease(ctx, local, manager, current, directory, enabled)
	})
	return enabled, result
}

// The caller owns the lifecycle session and has quiesced a replaced service.
func activateStoppedRelease(ctx context.Context, local support, manager lifecycleService, installed *service.Config, directory string, enabled bool) error {
	if installed != nil && installed.BinDir != directory {
		if err := manager.Uninstall(ctx); err != nil {
			return err
		}
		installed = nil
	}
	if _, err := manager.InstallWithState(ctx, directory, enabled); err != nil {
		if installed == nil || !errors.Is(err, service.ErrAlreadyInstalled) {
			return err
		}
	}
	if !enabled {
		return nil
	}
	if err := manager.Start(ctx); err != nil && !errors.Is(err, service.ErrServiceLoaded) {
		return err
	}
	_, err := waitForRuntime(ctx, local.inspect, false)
	return err
}

func checkManagedSwitch(installed *service.Config, directory string) error {
	if installed == nil {
		return nil
	}
	root, err := distribution.DefaultRoot()
	if err != nil {
		return err
	}
	previous, next := distribution.ManagedVersion(root, installed.BinDir), distribution.ManagedVersion(root, directory)
	if previous == "" || next == "" {
		return nil
	}
	order, err := distribution.CompareVersions(next, previous)
	if err != nil {
		return err
	}
	if order < 0 {
		return humanHint("managed downgrades are not supported")
	}
	return nil
}

func verifyReleasePrograms(ctx context.Context, directory, version string, run func(context.Context, string, ...string) ([]byte, error)) error {
	var commit string
	for _, name := range []string{"telegram-mcp", "telegram-mcpd"} {
		output, err := run(ctx, filepath.Join(directory, distribution.Binary(name)), "--version")
		if err != nil {
			return err
		}
		prefix := "Telegram MCP " + name + " version=" + version + " commit="
		line := strings.TrimSuffix(string(output), "\n")
		if !strings.HasPrefix(line, prefix) {
			return humanHint("release executable identity mismatch")
		}
		revision := strings.TrimPrefix(line, prefix)
		if len(revision) != 40 || strings.Trim(revision, "0123456789abcdef") != "" {
			return humanHint("invalid release executable revision")
		}
		if commit != "" && revision != commit {
			return humanHint("mixed release executables")
		}
		commit = revision
	}
	return nil
}

// Human subcommands from the stable entry delegate to the selected release.
// No-argument MCP traffic never enters this function.
func dispatchControl(ctx context.Context, args []string, stdout, stderr io.Writer) (bool, int) {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "telegram-mcp: cannot resolve control executable")
		return true, 1
	}
	root, err := distribution.DefaultRoot()
	if err != nil {
		fmt.Fprintln(stderr, "telegram-mcp: cannot resolve installation directory")
		return true, 1
	}
	if executable != filepath.Join(root, distribution.Binary("telegram-mcp")) {
		return false, 0
	}
	if len(args) == 0 || args[0] == "install" || args[0] == "--help" || args[0] == "-h" {
		return false, 0
	}
	if err := privatefs.CheckExecutable(executable); err != nil {
		writeSupportError(stderr, err)
		return true, 1
	}
	local, err := defaultSupport()
	if err != nil {
		writeSupportError(stderr, err)
		return true, 1
	}
	installed, err := local.service.Inspect(ctx)
	if err != nil {
		writeSupportError(stderr, err)
		return true, 1
	}
	if installed == nil {
		if args[0] == "upgrade" || args[0] == "doctor" || args[0] == "agent-config" || args[0] == "--version" {
			return false, 0
		}
		fmt.Fprintln(stderr, "telegram-mcp: no active version; run install --version X.Y.Z --setup")
		return true, 1
	}
	if distribution.ManagedVersion(root, installed.BinDir) == "" {
		fmt.Fprintln(stderr, "telegram-mcp: existing service is unmanaged; use upgrade --version X.Y.Z to adopt it")
		if args[0] == "upgrade" {
			return false, 0
		}
		return true, 1
	}
	command := exec.CommandContext(ctx, installed.Relay, args...)
	command.WaitDelay = 5 * time.Second
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return true, exit.ExitCode()
		}
		fmt.Fprintln(stderr, "telegram-mcp: active control program failed to start")
		return true, 1
	}
	return true, 0
}
