package control

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

type upgradeService struct {
	setupService
	fail string
}

func (f *upgradeService) Stop(context.Context) error {
	f.actions = append(f.actions, "stop")
	if f.fail == "stop" {
		return errors.New("stop failed")
	}
	return nil
}
func (f *upgradeService) Uninstall(context.Context) error {
	f.actions = append(f.actions, "uninstall")
	if f.fail == "uninstall" {
		return errors.New("uninstall failed")
	}
	return nil
}
func (f *upgradeService) Install(_ context.Context, dir string) (service.Config, error) {
	f.actions = append(f.actions, "install")
	if f.fail == "install" {
		return service.Config{}, errors.New("install failed")
	}
	return service.Config{BinDir: dir}, nil
}
func (f *upgradeService) Start(context.Context) error {
	f.actions = append(f.actions, "start")
	if f.fail == "start" {
		return errors.New("start failed")
	}
	return nil
}

func TestUpgradeActivationStopsAtEachFailedBoundary(t *testing.T) {
	all := []string{"stop", "uninstall", "install", "start"}
	for n, failed := range append(append([]string{}, all...), "") {
		manager := &upgradeService{fail: failed}
		probed := false
		local := support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
			probed = true
			return diagnostics.Report{MCP: true}, nil
		}}
		err := activateRelease(context.Background(), local, &service.Config{BinDir: "old"}, "new")
		want := all
		if failed != "" {
			want = all[:n+1]
		}
		if !reflect.DeepEqual(manager.actions, want) {
			t.Fatal(manager.actions, want)
		}
		if failed != "" && (err == nil || probed) {
			t.Fatal("activation continued after failure")
		}
		if failed == "" && (err != nil || !probed) {
			t.Fatal(err)
		}
	}
}
func TestUpgradeSameVersionDoesNotUninstall(t *testing.T) {
	manager := &upgradeService{}
	local := support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) { return diagnostics.Report{MCP: true}, nil }}
	if err := activateRelease(context.Background(), local, &service.Config{BinDir: "same"}, "same"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.actions, []string{"install", "start"}) {
		t.Fatal(manager.actions)
	}
}
func TestReleaseIdentityChecksEveryProgram(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		count := 0
		run := func(_ context.Context, name string, args ...string) ([]byte, error) {
			count++
			if !reflect.DeepEqual(args, []string{"--version"}) {
				t.Fatal(args)
			}
			component := strings.TrimSuffix(filepath.Base(name), ".exe")
			commit := strings.Repeat("a", 40)
			if mismatch && component == "telegram-mcpd" {
				commit = strings.Repeat("b", 40)
			}
			return []byte("Telegram MCP " + component + " version=0.2.0 commit=" + commit + "\n"), nil
		}
		err := verifyReleasePrograms(context.Background(), "/private/bin", "0.2.0", run)
		if (err != nil) != mismatch || count != 2 {
			t.Fatal(err, count)
		}
	}
}
func TestManagedVersionComparison(t *testing.T) {
	for _, pair := range [][2]string{{"0.2.0", "0.1.9"}, {"0.10.0", "0.9.0"}, {"100000000000000000000.0.0", "2.0.0"}} {
		if n, err := distribution.CompareVersions(pair[0], pair[1]); err != nil || n <= 0 {
			t.Fatal(n, err)
		}
	}
	if _, err := distribution.CompareVersions("0.02.0", "0.2.0"); err == nil {
		t.Fatal("accepted noncanonical version")
	}
}
