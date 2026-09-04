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
		fmt.Fprintln(stdout, buildinfo.String("tg-context-mcp"))
		return 0
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "usage: tg-context-mcp --version")
		fmt.Fprintln(stdout, "Phase 1 runtime: no MCP transport or tools are enabled.")
		return 0
	}

	// stdout is reserved for MCP frames, including while the relay is absent.
	fmt.Fprintln(stderr, "tg-context-mcp: unavailable in Phase 1; no MCP transport is enabled")
	return 2
}
