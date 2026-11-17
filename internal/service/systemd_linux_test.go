package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

type systemdRunner struct {
	state            systemdUnitState
	calls            [][]string
	output           []byte
	queryError       error
	runError         error
	ignoreModeChange bool
}

func (r *systemdRunner) Run(ctx context.Context, program string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.calls = append(r.calls, append([]string{program}, args...))
	if r.runError != nil {
		return r.runError
	}
	if program != "/usr/bin/systemctl" || len(args) != 3 || args[0] != "--user" {
		if program == "/usr/bin/systemctl" && reflect.DeepEqual(args, []string{"--user", "daemon-reload"}) {
			r.state.load, r.state.needsReload = "loaded", "no"
			return nil
		}
		return errors.New("unexpected synthetic systemctl command")
	}
	if args[2] != Label+".service" {
		return errors.New("unexpected synthetic unit")
	}
	switch args[1] {
	case "enable":
		if !r.ignoreModeChange {
			r.state.unitFile = "enabled"
		}
	case "disable":
		if !r.ignoreModeChange {
			r.state.unitFile = "disabled"
		}
	case "start":
		r.state.active = "active"
	case "stop":
		r.state.active = "inactive"
	default:
		return errors.New("unexpected synthetic unit mutation")
	}
	return nil
}

func (r *systemdRunner) Query(ctx context.Context, program string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.calls = append(r.calls, append([]string{program}, args...))
	if r.queryError != nil {
		return nil, r.queryError
	}
	expected := []string{"--user", "show", "--no-pager", "--all",
		"--property=LoadState,UnitFileState,ActiveState,FragmentPath,DropInPaths,NeedDaemonReload", Label + ".service"}
	if program != "/usr/bin/systemctl" || !reflect.DeepEqual(args, expected) {
		return nil, errors.New("unexpected synthetic systemctl query")
	}
	if r.output != nil {
		return r.output, nil
	}
	s := r.state
	return []byte(fmt.Sprintf("LoadState=%s\nUnitFileState=%s\nActiveState=%s\nFragmentPath=%s\nDropInPaths=%s\nNeedDaemonReload=%s\n",
		s.load, s.unitFile, s.active, s.fragment, s.dropIns, s.needsReload)), nil
}

func systemdFixture(t *testing.T) (*Manager, *systemdRunner, Config) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("per-user service excludes root")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home café")
	if err := privatefs.EnsureDirectory(home); err != nil {
		t.Fatal(err)
	}
	runner := &systemdRunner{}
	manager, err := New(home, os.Geteuid(), runner)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, "version % $ & binaries")
	if err := privatefs.EnsureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	config := binaryConfig(directory)
	for _, path := range []string{config.Relay, config.Daemon} {
		if err := privatefs.WriteExecutable(path, []byte("synthetic executable")); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureDirectory(filepath.Dir(manager.unitPath()), manager.uid, false); err != nil {
		t.Fatal(err)
	}
	if err := writeExclusive(manager.unitPath(), manager.unit(config)); err != nil {
		t.Fatal(err)
	}
	runner.state = systemdUnitState{
		load: "loaded", unitFile: "enabled", active: "inactive",
		fragment: manager.unitPath(), needsReload: "no",
	}
	return manager, runner, config
}

func systemdMutations(runner *systemdRunner) []string {
	var mutations []string
	for _, call := range runner.calls {
		if len(call) > 2 && call[2] != "show" {
			mutations = append(mutations, call[2])
		}
	}
	return mutations
}

func TestSystemdAutomaticActivationRequiresCanonicalEnabledUnit(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*systemdUnitState)
		start  bool
		want   error
	}{
		{name: "inactive", start: true},
		{name: "failed", change: func(s *systemdUnitState) { s.active = "failed" }, start: true},
		{name: "active", change: func(s *systemdUnitState) { s.active = "active" }},
		{name: "activating", change: func(s *systemdUnitState) { s.active = "activating" }},
		{name: "reloading", change: func(s *systemdUnitState) { s.active = "reloading" }},
		{name: "disabled", change: func(s *systemdUnitState) { s.unitFile = "disabled" }, want: ErrDisabled},
		{name: "masked", change: func(s *systemdUnitState) { s.load, s.unitFile, s.fragment = "masked", "masked", "/dev/null" }, want: ErrDisabled},
		{name: "runtime mask", change: func(s *systemdUnitState) { s.unitFile = "masked-runtime" }, want: ErrDisabled},
		{name: "missing", change: func(s *systemdUnitState) { s.load, s.unitFile, s.fragment = "not-found", "", "" }, want: ErrServiceAbsent},
		{name: "contradictory absence", change: func(s *systemdUnitState) { s.load = "not-found" }, want: ErrInvalidInstallation},
		{name: "different fragment", change: func(s *systemdUnitState) { s.fragment = "/different/unit.service" }, want: ErrInvalidInstallation},
		{name: "drop-in", change: func(s *systemdUnitState) { s.dropIns = "/different/override.conf" }, want: ErrInvalidInstallation},
		{name: "reload needed", change: func(s *systemdUnitState) { s.needsReload = "yes" }, want: ErrInvalidInstallation},
		{name: "runtime enablement", change: func(s *systemdUnitState) { s.unitFile = "enabled-runtime" }, want: ErrInvalidInstallation},
		{name: "static", change: func(s *systemdUnitState) { s.unitFile = "static" }, want: ErrInvalidInstallation},
		{name: "linked", change: func(s *systemdUnitState) { s.unitFile = "linked" }, want: ErrInvalidInstallation},
		{name: "generated", change: func(s *systemdUnitState) { s.unitFile = "generated" }, want: ErrInvalidInstallation},
		{name: "stopping", change: func(s *systemdUnitState) { s.active = "deactivating" }, want: ErrServiceUnavailable},
		{name: "unknown activity", change: func(s *systemdUnitState) { s.active = "unknown" }, want: ErrServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, runner, _ := systemdFixture(t)
			if test.change != nil {
				test.change(&runner.state)
			}
			err := manager.wake(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("activation: got %v, want %v", err, test.want)
			}
			want := []string(nil)
			if test.start {
				want = []string{"start"}
			}
			if got := systemdMutations(runner); !reflect.DeepEqual(got, want) {
				t.Fatalf("unexpected automatic mutations: %v, want %v", got, want)
			}
		})
	}
}

func TestSystemdQueriesRejectIncompleteOrAmbiguousOutput(t *testing.T) {
	for _, output := range []string{
		"",
		"LoadState=loaded\n",
		"LoadState=loaded\nLoadState=loaded\nUnitFileState=enabled\nActiveState=inactive\nFragmentPath=/unit\nDropInPaths=\nNeedDaemonReload=no\n",
		"LoadState=loaded\nUnitFileState=enabled\nActiveState=inactive\nFragmentPath=/unit\nDropInPaths=\nNeedDaemonReload=no\nOther=value\n",
		"LoadState=loaded\nUnitFileState=enabled\nActiveState=inactive\nFragmentPath=/unit\nDropInPaths=\nNeedDaemonReload=no\n\n",
		strings.Repeat("x", maxQueryBytes+1),
	} {
		t.Run(fmt.Sprintf("bytes_%d", len(output)), func(t *testing.T) {
			manager, runner, _ := systemdFixture(t)
			runner.output = []byte(output)
			if _, err := manager.enabled(context.Background()); !errors.Is(err, ErrServiceUnavailable) {
				t.Fatalf("ambiguous output accepted: %v", err)
			}
			if len(systemdMutations(runner)) != 0 {
				t.Fatal("query mutated service")
			}
		})
	}
}

func TestSystemdStateDistinguishesPersistentPauseAndMissingRegistration(t *testing.T) {
	manager, runner, _ := systemdFixture(t)
	runner.state.unitFile = "disabled"
	enabled, err := manager.enabled(context.Background())
	if err != nil || enabled {
		t.Fatalf("disabled state: %v %v", enabled, err)
	}
	registered, err := manager.registrationExists(context.Background())
	if err != nil || !registered {
		t.Fatalf("paused registration reported absent: %v %v", registered, err)
	}
	runner.state.load, runner.state.unitFile, runner.state.fragment = "not-found", "", ""
	registered, err = manager.registrationExists(context.Background())
	if err != nil || registered {
		t.Fatalf("missing registration: %v %v", registered, err)
	}
	if _, err := manager.enabled(context.Background()); !errors.Is(err, ErrServiceAbsent) {
		t.Fatalf("positive registration absence was hidden: %v", err)
	}
	if err := manager.setEnabled(context.Background(), false); !errors.Is(err, ErrServiceAbsent) {
		t.Fatalf("absent registration was implicitly repaired: %v", err)
	}
}

func TestSystemdEnablementChangeRequiresConfirmedPersistentResult(t *testing.T) {
	for _, enable := range []bool{false, true} {
		for _, applied := range []bool{false, true} {
			t.Run(fmt.Sprintf("enabled_%v_applied_%v", enable, applied), func(t *testing.T) {
				manager, runner, _ := systemdFixture(t)
				runner.state.unitFile = "enabled"
				if enable {
					runner.state.unitFile = "disabled"
				}
				runner.ignoreModeChange = !applied
				err := manager.setEnabled(context.Background(), enable)
				if applied && err != nil {
					t.Fatal(err)
				}
				if !applied && !errors.Is(err, ErrServiceUnavailable) {
					t.Fatalf("unconfirmed mode change reported success: %v", err)
				}
				action := "disable"
				if enable {
					action = "enable"
				}
				if got := systemdMutations(runner); !reflect.DeepEqual(got, []string{action}) {
					t.Fatalf("unexpected lifecycle mutations: %v", got)
				}
			})
		}
	}
}

func TestSystemdDisabledInstallationNeverEnablesOrStarts(t *testing.T) {
	manager, runner, config := systemdFixture(t)
	if err := os.Remove(manager.unitPath()); err != nil {
		t.Fatal(err)
	}
	runner.state.load, runner.state.unitFile = "not-found", "disabled"
	if err := manager.installRegistrationState(context.Background(), config, false); err != nil {
		t.Fatal(err)
	}
	if got := systemdMutations(runner); !reflect.DeepEqual(got, []string{"daemon-reload", "disable"}) {
		t.Fatalf("disabled install: %v", got)
	}
	if runner.state.unitFile != "disabled" {
		t.Fatal("disabled install changed enablement")
	}
}

func TestSystemdFailuresRetainCausesAndDoNotMutate(t *testing.T) {
	manager, runner, _ := systemdFixture(t)
	cause := errors.New("synthetic unavailable user bus")
	runner.queryError = cause
	if _, err := manager.enabled(context.Background()); !errors.Is(err, cause) || !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("lost query cause: %v", err)
	}
	runner.queryError, runner.runError = nil, cause
	if err := manager.wake(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("lost start cause: %v", err)
	}
	if got := systemdMutations(runner); !reflect.DeepEqual(got, []string{"start"}) {
		t.Fatalf("unexpected fallback: %v", got)
	}
}

func TestSystemdExternalMaskIsNeverRemoved(t *testing.T) {
	manager, runner, _ := systemdFixture(t)
	runner.state.load, runner.state.unitFile, runner.state.fragment = "masked", "masked", "/dev/null"
	enabled, err := manager.enabled(context.Background())
	if err != nil || enabled {
		t.Fatalf("mask state: %v %v", enabled, err)
	}
	for _, desired := range []bool{false, true} {
		if err := manager.setEnabled(context.Background(), desired); !errors.Is(err, ErrDisabled) {
			t.Fatalf("mask was treated as mutable enablement: %v", err)
		}
	}
	if len(systemdMutations(runner)) != 0 {
		t.Fatal("external mask was changed")
	}
}

func TestSystemdStopKeepsDisabledStateAndDoesNotRequireHealthyBinaries(t *testing.T) {
	manager, runner, config := systemdFixture(t)
	runner.state.active = "active"
	if err := os.Remove(config.Daemon); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.state.unitFile != "disabled" || runner.state.active != "inactive" {
		t.Fatalf("stop did not persist: %+v", runner.state)
	}
	if err := manager.wake(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("paused daemon woke: %v", err)
	}
}
