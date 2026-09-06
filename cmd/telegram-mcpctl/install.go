package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lstpsche/telegram-mcp/internal/distribution"
)

func runInstallCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	setup := len(args) == 4 && args[3] == "--setup"
	if (len(args) != 3 && !setup) || args[1] != "--version" || !distribution.ValidVersion(args[2]) {
		fmt.Fprintln(stderr, "telegram-mcpctl: use install --version X.Y.Z [--setup]")
		return 2
	}
	installer, err := distribution.Default()
	var directory string
	if err == nil {
		directory, err = installer.Install(ctx, args[2])
	}
	if err != nil {
		fmt.Fprintln(stderr, "telegram-mcpctl: release installation failed; check the version, network, checksums and private installation directory. Existing versions were not replaced.")
		return 1
	}
	if err := writeTextJSON(stdout, struct {
		Directory string `json:"directory"`
	}{directory}); err != nil {
		return 1
	}
	if setup {
		command := exec.CommandContext(ctx, filepath.Join(directory, distribution.Binary("telegram-mcpctl")), "setup")
		command.Stdin = os.Stdin
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, "Release installed. Run telegram-mcpctl setup from that directory to configure the account and service.")
	return 0
}
