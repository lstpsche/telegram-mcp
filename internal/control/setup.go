package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	operatorcli "github.com/lstpsche/telegram-mcp/internal/cli"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/service"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

type setupTerminal interface {
	terminal
	Ask(context.Context, string) (string, error)
}

type setupSession struct {
	control controller
	local   support
	prompt  setupTerminal
	output  io.Writer
	binDir  string
	command func(context.Context, string, ...string) ([]byte, error)
}

func runSetupCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "telegram-mcp: setup accepts no arguments")
		return 2
	}
	err := setupDefault(ctx, stdout, false)
	if err != nil {
		fmt.Fprintln(stderr, "telegram-mcp: setup stopped; completed changes were retained. Use status and doctor before resuming setup.")
		writeControlError(stderr, err)
		return 1
	}
	return 0
}

func setupDefault(ctx context.Context, output io.Writer, accessOnly bool) (result error) {
	local, err := defaultSupport()
	if err != nil {
		return err
	}
	control, err := defaultController()
	if err != nil {
		return err
	}
	prompt, err := operatorcli.OpenTerminal()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, prompt.Close()) }()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	s := setupSession{control: control, local: local, prompt: prompt, output: output, binDir: filepath.Dir(executable), command: runClientCommand}
	if accessOnly {
		return s.runAccessSetup(ctx)
	}
	return s.run(ctx)
}

func (s *setupSession) confirm(ctx context.Context, question string) error {
	answer, err := s.prompt.Ask(ctx, question+" Type yes to continue: ")
	if err != nil {
		return err
	}
	if answer != "yes" {
		return context.Canceled
	}
	return nil
}

func (s *setupSession) run(ctx context.Context) error {
	installed, err := s.local.service.Inspect(ctx)
	if err != nil {
		return err
	}
	if installed != nil {
		root, err := distribution.DefaultRoot()
		if err != nil {
			return err
		}
		previous, next := distribution.ManagedVersion(root, installed.BinDir), distribution.ManagedVersion(root, s.binDir)
		if previous != "" && next != "" {
			order, err := distribution.CompareVersions(next, previous)
			if err != nil {
				return err
			}
			if order < 0 {
				return humanHint("managed downgrades are not supported")
			}
		}
	}
	report, err := s.local.inspect(ctx)
	if err != nil {
		return err
	}
	status, err := s.control.Status(ctx)
	if err != nil {
		return err
	}
	if report.MCP || status.Daemon == daemon.SocketLive {
		if installed == nil {
			return humanHint("stop the foreground daemon before setup")
		}
		if err := s.confirm(ctx, "Setup needs to stop the service temporarily. Connected agents will need to reconnect."); err != nil {
			return err
		}
		if err := s.local.service.Stop(ctx); err != nil {
			return err
		}
	}
	if !status.Configured {
		environment, err := s.prompt.Ask(ctx, "Account environment: production, test1, test2, or test3: ")
		if err != nil {
			return err
		}
		var args []string
		switch environment {
		case "production":
			if err := s.confirm(ctx, "Confirm eligibility under Telegram terms and the rights and consent of people affected. This is your attestation, not an exemption."); err != nil {
				return err
			}
			args = []string{"--production", "--attest-eligible"}
		case "test1", "test2", "test3":
			args = []string{"--test-dc", environment[4:]}
		default:
			return humanHint("choose an explicit account environment")
		}
		env, dc, ok := parseConfiguration(args)
		if !ok {
			return humanHint("invalid account environment")
		}
		if err := s.control.Configure(ctx, env, dc, func(ctx context.Context) (int, []byte, error) {
			id, err := s.prompt.ReadAPIID(ctx)
			if err != nil {
				return 0, nil, err
			}
			hash, err := s.prompt.ReadAPIHash(ctx)
			return id, hash, err
		}); err != nil {
			return err
		}
	}
	needsAuth := !status.Authorized || (report.AccountState != nil && *report.AccountState == daemon.StateReauthRequired)
	if needsAuth {
		method, err := s.prompt.Ask(ctx, "Sign in with phone or qr: ")
		if err != nil {
			return err
		}
		if method != "phone" && method != "qr" {
			return humanHint("choose phone or qr authentication")
		}
		if _, err := s.control.Authenticate(ctx, tgaccount.AuthMethod(method), s.prompt); err != nil {
			return err
		}
	}
	if err := s.chooseAccess(ctx); err != nil {
		return err
	}
	if err := s.chooseScope(ctx); err != nil {
		return err
	}
	if installed != nil && installed.BinDir != s.binDir {
		if err := s.confirm(ctx, "Switch the service to the binaries running this setup? Existing binaries and account data will be retained."); err != nil {
			return err
		}
	}
	if err := activateRelease(ctx, s.local, installed, s.binDir); err != nil {
		return err
	}
	installed, err = s.local.service.Inspect(ctx)
	if err != nil {
		return err
	}
	if installed == nil {
		return service.ErrNotInstalled
	}
	if _, err := waitForReady(ctx, s.local.inspect); err != nil {
		return err
	}
	relay, err := distribution.Relay(installed.BinDir, installed.Relay)
	if err != nil {
		return err
	}
	if err := s.connectClient(ctx, relay); err != nil {
		return err
	}
	_, err = fmt.Fprintln(s.output, "Setup complete: the service responds and account reads are ready. Access remains governed by your selected policy. Reconnect your client and ask it to check Telegram status.")
	return err
}

func (s *setupSession) chooseAccess(ctx context.Context) error {
	access, ok := s.control.(accessController)
	if !ok {
		return humanHint("access control unavailable")
	}
	full, err := access.FullRead(ctx)
	if err != nil {
		return err
	}
	mode := "restricted"
	if full {
		mode = "full"
	}
	if _, err = fmt.Fprintln(s.output, "Current access:", mode); err != nil {
		return err
	}
	choice, err := s.prompt.Ask(ctx, "Access: keep current settings [Enter], restricted, or full: ")
	if err != nil {
		return err
	}
	switch choice {
	case "":
		return nil
	case "restricted":
		if full {
			if err := access.SetFullRead(ctx, false); err != nil {
				return err
			}
		}
		return s.guideGrant(ctx, time.Now().UTC())
	case "full":
		if err := s.confirm(ctx, "Full read exposes all supported conversations, authors, history, images and PDF/plain-text attachments to the connected agent and model provider, and permits read acknowledgments. Named scopes do not restrict this authority."); err != nil {
			return err
		}
		return access.SetFullRead(ctx, true)
	default:
		return humanHint("invalid access choice")
	}
}

func waitForReady(ctx context.Context, inspect func(context.Context) (diagnostics.Report, error)) (diagnostics.Report, error) {
	return waitForRuntime(ctx, inspect, true)
}

func waitForRuntime(ctx context.Context, inspect func(context.Context) (diagnostics.Report, error), reads bool) (diagnostics.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		report, err := inspect(ctx)
		if err != nil {
			return diagnostics.Report{}, err
		}
		if report.MCP && (!reads || (report.MessageReads != nil && *report.MessageReads)) {
			return report, nil
		}
		if report.AccountState != nil && *report.AccountState == daemon.StateReauthRequired {
			return diagnostics.Report{}, tgaccount.ErrReauthenticationRequired
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return diagnostics.Report{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *setupSession) connectClient(ctx context.Context, relay string) error {
	choice, err := s.prompt.Ask(ctx, "Connect client: json [Enter] or codex: ")
	if err != nil {
		return err
	}
	if choice == "" || choice == "json" {
		return json.NewEncoder(s.output).Encode(map[string]any{"mcpServers": map[string]any{"telegram": map[string]string{"command": relay}}})
	}
	if choice != "codex" {
		return humanHint("choose json or codex")
	}
	// The supported client CLI owns its configuration format and persistence.
	data, err := s.command(ctx, "codex", "mcp", "list", "--json")
	if err != nil {
		return &humanStepError{hint: "could not inspect Codex registrations; install the Codex CLI or select json", cause: err}
	}
	var servers []struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Transport struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			EnvVars []string          `json:"env_vars"`
			Cwd     *string           `json:"cwd"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(data, &servers); err != nil || servers == nil {
		return humanHint("invalid Codex registration response")
	}
	for _, server := range servers {
		if server.Name == "telegram" {
			if server.Enabled && server.Transport.Type == "stdio" && server.Transport.Command == relay && len(server.Transport.Args) == 0 && len(server.Transport.Env) == 0 && len(server.Transport.EnvVars) == 0 && server.Transport.Cwd == nil {
				return nil
			}
			return humanHint("Codex already has a telegram registration; inspect it explicitly or select json")
		}
	}
	if err := writeTextJSON(s.output, struct{ Client, Name, Command string }{"codex", "telegram", relay}); err != nil {
		return err
	}
	if err := s.confirm(ctx, "Add this Telegram MCP server to Codex?"); err != nil {
		return err
	}
	_, err = s.command(ctx, "codex", "mcp", "add", "telegram", "--", relay)
	return err
}

func runClientCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = time.Second
	output := &boundedClientOutput{}
	command.Stdout = output
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("client command failed: %w", err)
	}
	return output.data, nil
}

type boundedClientOutput struct{ data []byte }

func (b *boundedClientOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 1024*1024 {
		return 0, humanHint("client response exceeds limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
