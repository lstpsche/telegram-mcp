package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

// The service manager and runtime probe are the operating-system seams. Support
// commands never construct the account controller or open an interactive terminal.
type serviceManager interface {
	Install(context.Context, string) (service.Config, error)
	Inspect(context.Context) (*service.Config, error)
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
	Uninstall(context.Context) error
}

type support struct {
	service serviceManager
	inspect func(context.Context) (diagnostics.Report, error)
}

func defaultSupport() (support, error) {
	manager, err := service.Default()
	if err != nil {
		return support{}, err
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return support{}, err
	}
	return support{service: manager, inspect: func(ctx context.Context) (diagnostics.Report, error) {
		return diagnostics.Inspect(ctx, paths)
	}}, nil
}

func isSupportCommand(command string) bool {
	return command == "service" || command == "doctor" || command == "agent-config"
}

func runSupportCommand(ctx context.Context, args []string, stdout, stderr io.Writer, create func() (support, error)) int {
	if !validSupportArguments(args) {
		fmt.Fprintln(stderr, "telegram-mcpctl: use service install --bin-dir ABSOLUTE_DIRECTORY, service {start|stop|restart|uninstall}, doctor, or agent-config")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	local, err := create()
	if err != nil {
		writeSupportError(stderr, err)
		return 1
	}
	if args[0] == "service" {
		return runServiceCommand(ctx, args[1:], stdout, stderr, local.service)
	}
	installed, err := local.service.Inspect(ctx)
	if err != nil {
		writeSupportError(stderr, err)
		return 1
	}
	if args[0] == "agent-config" {
		if installed == nil {
			fmt.Fprintln(stderr, "telegram-mcpctl: install the service before generating agent configuration")
			return 1
		}
		configuration := map[string]any{"mcpServers": map[string]any{"telegram": map[string]string{"command": installed.Relay}}}
		return writeSupportJSON(stdout, stderr, configuration)
	}
	report, err := local.inspect(ctx)
	if err != nil {
		writeSupportError(stderr, err)
		return 1
	}
	result := doctorResult{Installation: "absent", Runtime: report, NextAction: "install_service"}
	if installed != nil {
		result.Installation = "valid"
		result.NextAction = "check_service_startup"
	}
	if report.MCP && installed != nil {
		result.NextAction = "inspect_account_readiness"
		if report.MessageReads != nil && *report.MessageReads {
			result.NextAction = "connect_agent"
		}
	}
	if code := writeSupportJSON(stdout, stderr, result); code != 0 {
		return code
	}
	if installed == nil || !report.MCP {
		return 1
	}
	return 0
}

type doctorResult struct {
	Installation string             `json:"installation"`
	Runtime      diagnostics.Report `json:"runtime"`
	NextAction   string             `json:"next_action"`
}

func validSupportArguments(args []string) bool {
	if len(args) == 1 {
		return args[0] == "doctor" || args[0] == "agent-config"
	}
	if len(args) < 2 || args[0] != "service" {
		return false
	}
	if args[1] == "install" {
		return len(args) == 4 && args[2] == "--bin-dir" && args[3] != ""
	}
	return len(args) == 2 && (args[1] == "start" || args[1] == "stop" || args[1] == "restart" || args[1] == "uninstall")
}

func runServiceCommand(ctx context.Context, args []string, stdout, stderr io.Writer, manager serviceManager) int {
	var err error
	var message string
	switch args[0] {
	case "install":
		_, err = manager.Install(ctx, args[2])
		message = "Service installation recorded. Run service start to launch it."
	case "start":
		err = manager.Start(ctx)
		message = "Service launch submitted. Run doctor to check MCP readiness."
	case "stop":
		err = manager.Stop(ctx)
		message = "Service stopped for this login session. It will start at the next user login."
	case "restart":
		err = manager.Restart(ctx)
		message = "Service restart submitted. Run doctor to check MCP readiness."
	case "uninstall":
		err = manager.Uninstall(ctx)
		message = "Service installation removed. Binaries, metadata, and local credentials retained."
	}
	if err != nil {
		writeSupportError(stderr, err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, message); err != nil {
		writeSupportError(stderr, err)
		return 1
	}
	return 0
}

func writeSupportJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		writeSupportError(stderr, err)
		return 1
	}
	return 0
}

func writeSupportError(writer io.Writer, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(writer, "telegram-mcpctl: support operation cancelled; inspect the current service state before retrying")
	case errors.Is(err, daemon.ErrAccountLocked):
		fmt.Fprintln(writer, "telegram-mcpctl: another service operation is running; retry after it finishes")
	case errors.Is(err, service.ErrNotInstalled):
		fmt.Fprintln(writer, "telegram-mcpctl: service is not installed; use service install --bin-dir ABSOLUTE_DIRECTORY")
	case errors.Is(err, service.ErrAlreadyInstalled):
		fmt.Fprintln(writer, "telegram-mcpctl: installation already exists; inspect it before an explicit stop and uninstall")
	case errors.Is(err, service.ErrServiceLoaded):
		fmt.Fprintln(writer, "telegram-mcpctl: service is registered; use service stop before starting or uninstalling")
	case errors.Is(err, service.ErrServiceAbsent):
		fmt.Fprintln(writer, "telegram-mcpctl: service is not registered in this user session")
	case errors.Is(err, service.ErrUnsafePath), errors.Is(err, service.ErrInvalidInstallation):
		fmt.Fprintln(writer, "telegram-mcpctl: installation validation failed; check canonical paths, ownership, permissions, and the generated service configuration")
	default:
		fmt.Fprintln(writer, "telegram-mcpctl: local support check failed; check filesystem permissions, the user login session, and foreground daemon diagnostics")
	}
}
