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
		fmt.Fprintln(stdout, buildinfo.String("tg-contextd"))
		return 0
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "usage: tg-contextd --version")
		fmt.Fprintln(stdout, "Phase 0 scaffold: no Telegram runtime is enabled.")
		return 0
	}

	fmt.Fprintln(stderr, "tg-contextd: unavailable in Phase 0; no Telegram runtime is enabled")
	return 2
}
