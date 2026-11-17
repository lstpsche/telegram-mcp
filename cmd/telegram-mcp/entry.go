package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/logging"
	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

// runEntryInfo precedes control dispatch so a stable entry reports itself,
// rather than the program selected by its service registration.
func runEntryInfo(args []string, output, diagnostics io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "--entry-info" {
		return false, 0
	}
	if len(args) != 1 {
		fmt.Fprintln(diagnostics, "telegram-mcp: --entry-info accepts no additional arguments")
		return true, 2
	}
	executable, err := os.Executable()
	if err != nil || !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		fmt.Fprintln(diagnostics, "telegram-mcp: cannot identify the entry executable")
		return true, 1
	}
	if err := json.NewEncoder(output).Encode(distribution.EntryInfo{Protocol: distribution.EntryProtocol, Executable: executable}); err != nil {
		return true, 1
	}
	return true, 0
}

// dispatchManagedEntry only handles the immutable-path launcher. The selected
// relay receives the same startup deadline, then owns its stdio byte bridge.
func dispatchManagedEntry(ctx context.Context, input io.ReadCloser, output io.WriteCloser, diagnostics io.Writer, deadline time.Time) (bool, int) {
	executable, err := os.Executable()
	if err != nil {
		return entryFailure(ctx, diagnostics, err)
	}
	root, err := distribution.DefaultRoot()
	if err != nil {
		return entryFailure(ctx, diagnostics, err)
	}
	stable, err := isManagedEntry(executable, root)
	if err != nil {
		return entryFailure(ctx, diagnostics, err)
	}
	if !stable {
		return false, 0
	}
	inspection, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	manager, err := service.Default()
	if err != nil {
		return entryFailure(inspection, diagnostics, err)
	}
	selected, err := selectedEntry(inspection, executable, root, manager.Inspect)
	if err != nil {
		return entryFailure(inspection, diagnostics, err)
	}
	if err := inspection.Err(); err != nil {
		return entryFailure(inspection, diagnostics, err)
	}
	environment := entryEnvironment(os.Environ(), deadline)
	code, err := executeEntry(ctx, selected, environment, input, output, diagnostics)
	if err != nil {
		return entryFailure(ctx, diagnostics, err)
	}
	return true, code
}

func selectedEntry(ctx context.Context, executable, root string, inspect func(context.Context) (*service.Config, error)) (string, error) {
	stable, err := isManagedEntry(executable, root)
	if err != nil {
		return "", err
	}
	if !stable {
		return "", distribution.ErrEntryInvalid
	}
	if err := privatefs.CheckExecutable(executable); err != nil {
		return "", err
	}
	installed, err := inspect(ctx)
	if err != nil {
		return "", err
	}
	if installed == nil {
		return "", service.ErrNotInstalled
	}
	expected := filepath.Join(installed.BinDir, distribution.Binary("telegram-mcp"))
	if distribution.ManagedVersion(root, installed.BinDir) == "" || installed.Relay != expected || expected == executable {
		return "", distribution.ErrEntryInvalid
	}
	if err := privatefs.CheckExecutable(expected); err != nil {
		return "", err
	}
	return expected, ctx.Err()
}

func isManagedEntry(executable, root string) (bool, error) {
	expected := filepath.Join(root, distribution.Binary("telegram-mcp"))
	if executable == expected {
		return true, nil
	}
	if runtime.GOOS != "windows" || !strings.EqualFold(executable, expected) {
		return false, nil
	}
	physical, err := privatefs.InspectExecutable(executable)
	if err != nil {
		return false, err
	}
	target, err := privatefs.InspectExecutable(expected)
	if err != nil {
		return false, err
	}
	return os.SameFile(physical, target), nil
}

func entryEnvironment(environment []string, deadline time.Time) []string {
	result := make([]string, 0, len(environment)+1)
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		matches := key == startupDeadlineEnvironment
		if runtime.GOOS == "windows" {
			matches = strings.EqualFold(key, startupDeadlineEnvironment)
		}
		if !matches {
			result = append(result, value)
		}
	}
	return append(result, startupDeadlineEnvironment+"="+strconv.FormatInt(deadline.UnixMilli(), 10))
}

func entryFailure(ctx context.Context, diagnostics io.Writer, err error) (bool, int) {
	category := model.ErrorNotReady
	if errors.Is(ctx.Err(), context.Canceled) {
		category = model.ErrorCancelled
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	logging.New(diagnostics, nil).Error(ctx, logging.EventOperationFailed,
		logging.ComponentField(logging.ComponentRelay), logging.ErrorCategoryField(category),
		logging.StartupReasonField(startupReason(err)))
	return true, 1
}
