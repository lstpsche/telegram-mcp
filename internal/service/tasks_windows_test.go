package service

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

type taskScriptRunner struct {
	calls   []string
	results []error
}

func (r *taskScriptRunner) Run(ctx context.Context, program string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.calls = append(r.calls, strings.Join(append([]string{program}, args...), "\n"))
	if len(r.results) == 0 {
		return nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result
}

func taskScriptFixture(t *testing.T) (*Manager, *taskScriptRunner, Config) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := privatefs.EnsureDirectory(home); err != nil {
		t.Fatal(err)
	}
	runner := &taskScriptRunner{}
	manager, err := New(home, os.Geteuid(), runner)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "synthetic $ & ' café 测试")
	if err := privatefs.EnsureDirectory(bin); err != nil {
		t.Fatal(err)
	}
	config := binaryConfig(bin)
	for _, path := range []string{config.Relay, config.Daemon} {
		if err := privatefs.WriteFile(path, []byte("synthetic executable"), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := privatefs.EnsureDirectory(manager.stateDir()); err != nil {
		t.Fatal(err)
	}
	record, err := manager.task(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := privatefs.WriteFile(manager.taskPath(), record, false); err != nil {
		t.Fatal(err)
	}
	return manager, runner, config
}

func TestTaskStateDistinguishesDisabledReadyAndQueued(t *testing.T) {
	for _, state := range []int{taskRunning, taskReady, taskMissing, taskDisabled, taskQueued, 5} {
		t.Run(strconv.Itoa(state), func(t *testing.T) {
			m, runner, _ := taskScriptFixture(t)
			var result error
			if state != taskRunning {
				result = testExit(state)
			}
			runner.results = []error{result}
			got, err := m.taskState(context.Background())
			if state == 5 {
				if err == nil {
					t.Fatal("unknown scheduler state was accepted")
				}
				return
			}
			if err != nil || got != state {
				t.Fatalf("state=%d error=%v", got, err)
			}
			runner.results = []error{result}
			running, err := m.running(context.Background())
			want := state == taskRunning || state == taskQueued
			if err != nil || running != want {
				t.Fatalf("running=%v want=%v error=%v", running, want, err)
			}
		})
	}
}

func TestAutomaticTaskWakeRejectsDisabledAndModifiedTasks(t *testing.T) {
	for _, reason := range []struct {
		exit int
		want error
	}{
		{taskDisabled, ErrDisabled},
		{taskInvalid, ErrInvalidInstallation},
		{taskMissing, ErrServiceAbsent},
	} {
		m, runner, _ := taskScriptFixture(t)
		runner.results = []error{testExit(reason.exit)}
		err := m.wake(context.Background())
		if !errors.Is(err, reason.want) || len(runner.calls) != 1 {
			t.Fatalf("wake=%v calls=%d", err, len(runner.calls))
		}
		script := runner.calls[0]
		if strings.Contains(script, ".Enabled=") || strings.Contains(script, "RegisterTask(") {
			t.Fatal("automatic wake changed enablement or registration")
		}
	}
}

func TestTaskValidationCarriesExpectedPathAsData(t *testing.T) {
	m, runner, config := taskScriptFixture(t)
	if err := m.wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatal("unexpected helper process count")
	}
	script := runner.calls[0]
	encoded := base64.StdEncoding.EncodeToString([]byte(config.Daemon))
	if !strings.Contains(script, encoded) || strings.Contains(script, config.Daemon) {
		t.Fatal("expected executable path entered script source")
	}
	for _, required := range []string{
		"Resolve-Sid", "$principal.RunLevel -ne 0", "$principal.LogonType -ne 3",
		"$definition.Actions.Count -ne 1", "$action.Arguments", "$action.WorkingDirectory",
		"$definition.Triggers.Count -ne 1", "$settings.AllowDemandStart",
		"$settings.MultipleInstances -ne 2", "$settings.RestartCount -ne 0",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing scheduler validation %q", required)
		}
	}
}

func TestTaskEnabledClassificationPreservesFailures(t *testing.T) {
	for _, reason := range []struct {
		exit    int
		enabled bool
		want    error
	}{
		{0, true, nil},
		{taskDisabled, false, nil},
		{taskInvalid, false, ErrInvalidInstallation},
		{taskMissing, false, ErrServiceAbsent},
	} {
		m, runner, _ := taskScriptFixture(t)
		if reason.exit != 0 {
			runner.results = []error{testExit(reason.exit)}
		}
		enabled, err := m.enabled(context.Background())
		if enabled != reason.enabled || !errors.Is(err, reason.want) {
			t.Fatalf("enabled=%v error=%v", enabled, err)
		}
	}
	m, runner, _ := taskScriptFixture(t)
	cause := errors.New("synthetic scheduler failure")
	runner.results = []error{cause}
	if _, err := m.enabled(context.Background()); !errors.Is(err, cause) {
		t.Fatal("scheduler cause was lost", err)
	}
}

func TestPausedTaskRegistrationKeepsCanonicalRecord(t *testing.T) {
	m, runner, config := taskScriptFixture(t)
	if err := os.Remove(m.taskPath()); err != nil {
		t.Fatal(err)
	}
	runner.results = []error{testExit(taskMissing), nil}
	if err := m.installRegistrationState(context.Background(), config, false); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 ||
		!strings.Contains(runner.calls[1], "/t:Task/t:Settings/t:Enabled") ||
		!strings.Contains(runner.calls[1], ".InnerText='false'") {
		t.Fatal("task was not registered disabled from the beginning")
	}
	stored, err := m.readRegistration(context.Background())
	if err != nil || stored != config {
		t.Fatal("saved installation lost canonical identity", err)
	}
}

func TestTaskMutationHonorsCancellation(t *testing.T) {
	m, runner, _ := taskScriptFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.setEnabled(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatal("enablement ignored cancellation", err)
	}
	if len(runner.calls) != 0 {
		t.Fatal("cancelled operation contacted scheduler")
	}
}

func TestTaskStartupUsesOneValidatedQueryBeforeFinalWake(t *testing.T) {
	for _, test := range []struct {
		name        string
		results     []error
		disposition StartDisposition
		want        error
		calls       int
	}{
		{"ready", []error{testExit(taskReady), nil}, StartLaunched, nil, 2},
		{"active", []error{nil}, StartJoining, nil, 1},
		{"disabled", []error{testExit(taskDisabled)}, "", ErrDisabled, 1},
		{"modified", []error{testExit(taskInvalid)}, "", ErrInvalidInstallation, 1},
		{"missing", []error{testExit(taskMissing)}, "", ErrServiceAbsent, 1},
		{"changed before wake", []error{testExit(taskReady), testExit(taskInvalid)}, "", ErrInvalidInstallation, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, runner, _ := taskScriptFixture(t)
			runner.results = test.results
			var disposition StartDisposition
			err := m.WithSession(context.Background(), func(s *Session) error {
				var err error
				disposition, err = s.RequestStart(context.Background(), time.Now())
				return err
			})
			if disposition != test.disposition || !errors.Is(err, test.want) || len(runner.calls) != test.calls {
				t.Fatalf("disposition=%s error=%v calls=%d", disposition, err, len(runner.calls))
			}
			for _, script := range runner.calls {
				if !strings.Contains(script, "$definition.Actions.Count -ne 1") || !strings.Contains(script, "if (-not $task.Enabled)") {
					t.Fatal("startup query or final wake omitted task validation")
				}
				if strings.Contains(script, ".Enabled=") || strings.Contains(script, "RegisterTask(") {
					t.Fatal("automatic startup changed installation or permission")
				}
			}
		})
	}
}
