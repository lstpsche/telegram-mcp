package main

import (
	"fmt"
	"io"
	"os"

	"github.com/lstpsche/telegram-mcp/internal/buildinfo"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, buildinfo.String("tg-contextctl"))
		return 0
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		fmt.Fprintln(stdout, "usage: tg-contextctl --version")
		fmt.Fprintln(stdout, "Phase 0 scaffold: authentication and policy commands are not enabled.")
		return 0
	}

	fmt.Fprintln(stderr, "tg-contextctl: unknown command; Phase 0 exposes no account or policy operations")
	return 2
}
