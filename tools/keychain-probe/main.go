// keychain-probe is an isolated, destructive-to-its-own-temporary-item proof
// for Telegram MCP's native macOS Keychain adapter. It never prints secret bytes.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
)

const probeService = "dev.telegram-mcp.keychain.probe"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--bin-dir" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		os.Exit(runQualification(ctx, os.Args[1:], os.Stdout, os.Stderr))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var err error
	if len(os.Args) == 3 && os.Args[1] == "--worker" {
		err = runWorker(ctx, os.Args[2])
	} else if len(os.Args) == 1 {
		err = runParent(ctx)
	} else {
		err = errors.New("invalid probe invocation")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "keychain probe failed: %v\n", err)
		os.Exit(1)
	}
}

func runParent(ctx context.Context) (result error) {
	if runtime.GOOS != "darwin" {
		return keychain.ErrUnsupported
	}
	account, err := randomAccount()
	if err != nil {
		return err
	}
	store, err := keychain.New(probeService)
	if err != nil {
		return err
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Delete(cleanupContext, account); err != nil {
			result = errors.Join(result, fmt.Errorf("clean up probe item: %w", err))
		}
	}()

	initial := make([]byte, 32)
	defer clear(initial)
	if _, err := rand.Read(initial); err != nil {
		return fmt.Errorf("generate initial probe value: %w", err)
	}
	if err := store.Put(ctx, account, initial); err != nil {
		return err
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve probe executable: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	command := exec.CommandContext(ctx, executable, "--worker", account)
	command.Env = []string{
		"HOME=" + home,
		"LANG=C",
		"PATH=/usr/bin:/bin",
		"TMPDIR=/tmp",
	}
	// A nil Stdin gives the child the null device: no terminal or prompt path.
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("noninteractive child process: %w", err)
	}
	if string(output) != "worker-ok\n" {
		return errors.New("noninteractive child returned an unexpected result")
	}
	if _, err := store.Get(ctx, account); !errors.Is(err, keychain.ErrNotFound) {
		return errors.New("deleted probe item remained readable")
	}

	fmt.Fprintln(os.Stdout, "keychain probe passed: login-keychain noninteractive cross-process store/read/update/delete; file-fallback=false")
	return nil
}

func runWorker(ctx context.Context, account string) error {
	store, err := keychain.New(probeService)
	if err != nil {
		return err
	}
	initial, err := store.Get(ctx, account)
	if err != nil {
		return err
	}
	defer clear(initial)
	if len(initial) != 32 {
		return errors.New("initial probe value has an unexpected length")
	}

	replacement := make([]byte, 48)
	defer clear(replacement)
	if _, err := rand.Read(replacement); err != nil {
		return fmt.Errorf("generate replacement probe value: %w", err)
	}
	if err := store.Put(ctx, account, replacement); err != nil {
		return err
	}
	readBack, err := store.Get(ctx, account)
	if err != nil {
		return err
	}
	defer clear(readBack)
	if !bytes.Equal(readBack, replacement) {
		return errors.New("updated probe value did not round trip")
	}
	if err := store.Delete(ctx, account); err != nil {
		return err
	}
	if _, err := store.Get(ctx, account); !errors.Is(err, keychain.ErrNotFound) {
		return errors.New("worker could read probe item after deletion")
	}
	fmt.Fprintln(os.Stdout, "worker-ok")
	return nil
}

func randomAccount() (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate probe account: %w", err)
	}
	return "probe-" + hex.EncodeToString(random), nil
}
