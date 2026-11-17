package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"github.com/lstpsche/telegram-mcp/internal/service"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestOfflineCommandsAcquireLeaseAfterArgumentValidation(t *testing.T) {
	cases := []struct {
		args    []string
		control controller
	}{
		{[]string{"configure", "--test-dc", "2"}, &fakeController{}},
		{[]string{"auth", "phone"}, &fakeController{}},
		{[]string{"logout"}, &fakeController{}},
		{[]string{"peers"}, &fakeTextController{}},
		{[]string{"saved-message"}, &fakeTextController{}},
		{[]string{"topics", "--peer", "tgpeer:v1:channel:42"}, &topicController{}},
		{[]string{"folders", "--id", "2"}, &folderController{}},
		{[]string{"scope", "--name", "work", "--folder", "2"}, &folderController{}},
		{[]string{"backup", "--file", "/private/backup"}, &fakeMaintenance{controller: &fakeController{}}},
		{[]string{"restore", "--dry-run", "--file", "/private/backup"}, &fakeMaintenance{controller: &fakeController{}}},
		{[]string{"audit"}, &fakeMaintenance{controller: &fakeController{}}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			acquired := 0
			ctx := context.WithValue(context.Background(), offlineLeaseKey{}, offlineLease(func(context.Context, func(context.Context) error) error {
				acquired++
				return errors.New("synthetic private lease failure")
			}))
			var out, errout bytes.Buffer
			code := runContext(ctx, tc.args, &out, &errout, func() (controller, error) { return tc.control, nil }, func() (terminal, error) { return &fakeTerminal{}, nil })
			if code != 1 || acquired != 1 || out.Len() != 0 || strings.Contains(errout.String(), "synthetic private") {
				t.Fatalf("lease boundary: code=%d acquired=%d out=%q error=%q", code, acquired, out.String(), errout.String())
			}
		})
	}
	for _, args := range [][]string{{"configure"}, {"auth", "bad"}, {"logout", "extra"}, {"peers", "extra"}, {"topics", "--peer", "bad"}, {"folders", "--id", "bad"}, {"scope", "--name", "work", "--folder", "bad"}, {"restore", "--file", "/backup"}, {"audit", "purge"}} {
		ctx := context.WithValue(context.Background(), offlineLeaseKey{}, offlineLease(func(context.Context, func(context.Context) error) error {
			t.Fatal("invalid arguments acquired a lease")
			return nil
		}))
		var out, errout bytes.Buffer
		if code := runContext(ctx, args, &out, &errout, func() (controller, error) { return &fakeController{}, nil }, func() (terminal, error) { t.Fatal("invalid arguments opened a prompt"); return nil, nil }); code != 2 {
			t.Fatalf("invalid command: %v code %d", args, code)
		}
	}
}

func TestPolicyAndRetainedStatusRemainIndependentOfLifecycleLease(t *testing.T) {
	cases := []struct {
		args    []string
		control controller
	}{
		{[]string{"status"}, &fakeController{}},
		{[]string{"sending"}, &fakeSendingController{}},
		{[]string{"sending", "disable"}, &fakeSendingController{}},
		{[]string{"sending", "status", "--operation-id", retainedOperationID}, &fakeSendingController{}},
		{[]string{"access"}, &fakeAccessController{}},
		{[]string{"grants"}, &fakeTextController{}},
		{[]string{"scopes"}, &fakeScopeController{}},
		{[]string{"scope", "--name", "work"}, &fakeScopeController{}},
	}
	for _, tc := range cases {
		ctx := context.WithValue(context.Background(), offlineLeaseKey{}, offlineLease(func(context.Context, func(context.Context) error) error {
			t.Fatalf("online command attempted lifecycle acquisition: %v", tc.args)
			return nil
		}))
		var out, errout bytes.Buffer
		if code := runContext(ctx, tc.args, &out, &errout, func() (controller, error) { return tc.control, nil }, nil); code != 0 {
			t.Fatalf("command %v: code %d error %q", tc.args, code, errout.String())
		}
	}
}

func TestUpgradeOfPausedServiceNeverStartsOrWaitsForRuntime(t *testing.T) {
	manager := &upgradeService{}
	manager.disabled = true
	manager.installed = &service.Config{BinDir: "old"}
	local := support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
		t.Fatal("paused activation inspected runtime")
		return diagnostics.Report{}, nil
	}}
	if _, err := activateUpgrade(context.Background(), local, "new"); err != nil {
		t.Fatal(err)
	}
	if !manager.disabled || !reflect.DeepEqual(manager.actions, []string{"stop", "uninstall", "install"}) {
		t.Fatalf("paused activation changed intent: disabled=%t actions=%v", manager.disabled, manager.actions)
	}
}

func TestSetupRequiresExplicitResumeOfPausedService(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "decline", true: "resume"}[resume], func(t *testing.T) {
			c := &fakeAccessController{}
			c.status = app.Status{Configured: true, Authorized: true}
			c.authError = errors.New("unexpected authentication")
			manager := &setupService{}
			manager.installed = &service.Config{BinDir: "/private/bin", Relay: "/private/bin/telegram-mcp"}
			manager.disabled = true
			answers := []string{"no"}
			if resume {
				answers = []string{"yes", "", "", "json"}
			}
			ready := true
			s := setupSession{control: c, prompt: &setupAnswers{answers: answers}, output: io.Discard, binDir: "/private/bin", local: support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
				return diagnostics.Report{MCP: manager.active, MessageReads: &ready}, nil
			}}}
			err := s.run(context.Background())
			if resume {
				if err != nil || manager.disabled || !manager.active {
					t.Fatalf("resume: %v disabled=%t active=%t", err, manager.disabled, manager.active)
				}
			} else if !errors.Is(err, context.Canceled) || !manager.disabled || manager.active || len(manager.actions) != 0 {
				t.Fatalf("decline mutated service: %v %v", err, manager.actions)
			}
			if c.writes != 0 {
				t.Fatal("service intent altered authority")
			}
		})
	}
}

type arrivingService struct{ setupService }

func (f *arrivingService) withControlSession(_ context.Context, run func(lifecycleService) error) error {
	f.active = true
	return run(f)
}
func TestSetupRechecksActivityAfterAcquiringSession(t *testing.T) {
	c := &fakeAccessController{}
	c.status = app.Status{Configured: true, Authorized: true}
	manager := &arrivingService{}
	manager.installed = &service.Config{BinDir: "/private/bin", Relay: "/private/bin/telegram-mcp"}
	s := setupSession{control: c, prompt: &setupAnswers{answers: []string{"no"}}, output: io.Discard, binDir: "/private/bin", local: support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) { return diagnostics.Report{MCP: manager.active}, nil }}}
	if err := s.run(context.Background()); !errors.Is(err, context.Canceled) || len(manager.actions) != 0 {
		t.Fatalf("newly active service was stopped without consent: %v %v", err, manager.actions)
	}
}

type accessGuideController struct{ fakeTextController }

func (*accessGuideController) FullRead(context.Context) (bool, error) { return false, nil }
func (*accessGuideController) SetFullRead(context.Context, bool) error {
	return errors.New("unexpected policy change")
}
func TestAccessSetupPreservesEnabledAndRunningIntent(t *testing.T) {
	for _, state := range []struct {
		name            string
		enabled, active bool
	}{
		{"paused", false, false}, {"enabled inactive", true, false}, {"running", true, true},
	} {
		t.Run(state.name, func(t *testing.T) {
			c := &accessGuideController{}
			c.status = app.Status{Configured: true, Authorized: true}
			manager := &setupService{active: state.active}
			manager.disabled = !state.enabled
			manager.installed = &service.Config{BinDir: "/private/bin", Relay: "/private/bin/telegram-mcp"}
			answers := []string{"saved", "yes", "24", "0", "no", "no", "no", "yes"}
			if state.active {
				answers = append([]string{"yes"}, answers...)
			}
			ready := true
			s := setupSession{control: c, prompt: &setupAnswers{answers: answers}, output: io.Discard, local: support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
				return diagnostics.Report{MCP: manager.active, MessageReads: &ready}, nil
			}}}
			if err := s.runAccessSetup(context.Background()); err != nil {
				t.Fatal(err)
			}
			if manager.disabled == state.enabled || manager.active != state.active {
				t.Fatalf("intent changed: disabled=%t active=%t", manager.disabled, manager.active)
			}
			want := []string{"stop"}
			if state.active {
				want = append(want, "start")
			}
			if !reflect.DeepEqual(manager.actions, want) {
				t.Fatalf("unexpected activation: %v", manager.actions)
			}
		})
	}
}

func TestInterruptedUpgradeDoesNotInferResumeFromMissingRegistration(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "previously paused", true: "previously enabled"}[enabled], func(t *testing.T) {
			manager := &upgradeService{fail: "install"}
			manager.installed = &service.Config{BinDir: "old"}
			manager.disabled = !enabled
			local := support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
				t.Fatal("partial activation must not wait for runtime")
				return diagnostics.Report{}, nil
			}}
			if _, err := activateUpgrade(context.Background(), local, "new"); err == nil || manager.installed != nil {
				t.Fatalf("expected removed registration and failed replacement: %v %+v", err, manager.installed)
			}
			manager.fail = ""
			manager.actions = nil
			resumed, err := activateUpgrade(context.Background(), local, "new")
			if err != nil || resumed || !manager.disabled || !reflect.DeepEqual(manager.actions, []string{"install"}) {
				t.Fatalf("retry inferred prior enablement: %v resumed=%t disabled=%t actions=%v", err, resumed, manager.disabled, manager.actions)
			}
		})
	}
}

type missingBackendService struct{ upgradeService }

func (*missingBackendService) Enabled(context.Context) (bool, error) {
	return false, service.ErrServiceAbsent
}
func (f *missingBackendService) withControlSession(_ context.Context, run func(lifecycleService) error) error {
	return run(f)
}
func TestExplicitUpgradeRepairsProvenMissingBackendDisabled(t *testing.T) {
	manager := &missingBackendService{}
	manager.installed = &service.Config{BinDir: "same"}
	local := support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
		t.Fatal("disabled repair inspected runtime")
		return diagnostics.Report{}, nil
	}}
	enabled, err := activateUpgrade(context.Background(), local, "same")
	if err != nil || enabled || !manager.disabled || !reflect.DeepEqual(manager.actions, []string{"install"}) {
		t.Fatalf("repair: %v enabled=%t disabled=%t actions=%v", err, enabled, manager.disabled, manager.actions)
	}
}

type setupMissingBackend struct{ setupService }

func (*setupMissingBackend) Enabled(context.Context) (bool, error) {
	return false, service.ErrServiceAbsent
}
func (f *setupMissingBackend) withControlSession(_ context.Context, run func(lifecycleService) error) error {
	return run(f)
}
func TestSetupCanResumeProvenMissingBackendAfterConfirmation(t *testing.T) {
	c := &fakeAccessController{}
	c.status = app.Status{Configured: true, Authorized: true}
	manager := &setupMissingBackend{}
	manager.installed = &service.Config{BinDir: "/private/bin", Relay: "/private/bin/telegram-mcp"}
	ready := true
	s := setupSession{control: c, prompt: &setupAnswers{answers: []string{"yes", "", "", "json"}}, output: io.Discard, binDir: "/private/bin", local: support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
		return diagnostics.Report{MCP: manager.active, MessageReads: &ready}, nil
	}}}
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.disabled || !manager.active || !reflect.DeepEqual(manager.actions, []string{"install", "start"}) {
		t.Fatalf("repair failed: disabled=%t active=%t actions=%v", manager.disabled, manager.active, manager.actions)
	}
}

func TestAccessSetupDoesNotRepairMissingBackend(t *testing.T) {
	c := &accessGuideController{}
	c.status = app.Status{Configured: true, Authorized: true}
	manager := &setupMissingBackend{}
	manager.installed = &service.Config{BinDir: "/private/bin", Relay: "/private/bin/telegram-mcp"}
	s := setupSession{control: c, prompt: &setupAnswers{answers: []string{"saved", "yes", "24", "0", "no", "no", "no", "yes"}}, output: io.Discard, local: support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) { return diagnostics.Report{}, nil }}}
	if err := s.runAccessSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(manager.actions) != 0 || manager.active {
		t.Fatalf("access setup repaired registration: %v", manager.actions)
	}
}

func TestMissingServiceBackendDoesNotAuthorizeStoppingForegroundRuntime(t *testing.T) {
	for _, guide := range []string{"setup", "access setup"} {
		t.Run(guide, func(t *testing.T) {
			c := &accessGuideController{}
			c.status = app.Status{Configured: true, Authorized: true}
			manager := &setupMissingBackend{}
			manager.active = true
			manager.installed = &service.Config{BinDir: "/private/bin", Relay: "/private/bin/telegram-mcp"}
			s := setupSession{control: c, prompt: &setupAnswers{answers: []string{"yes"}}, output: io.Discard, binDir: "/private/bin", local: support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) { return diagnostics.Report{MCP: true}, nil }}}
			var err error
			if guide == "setup" {
				err = s.run(context.Background())
			} else {
				err = s.runAccessSetup(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), "foreground daemon") || len(manager.actions) != 0 {
				t.Fatalf("foreground runtime accepted as managed: %v %v", err, manager.actions)
			}
		})
	}
}

type blockingAuthentication struct {
	fakeController
	entered chan struct{}
	release chan struct{}
}

func (c *blockingAuthentication) Authenticate(ctx context.Context, _ tgaccount.AuthMethod, _ tgaccount.Prompt) (app.AuthOutcome, error) {
	close(c.entered)
	select {
	case <-ctx.Done():
		return app.AuthOutcome{}, ctx.Err()
	case <-c.release:
		return app.AuthOutcome{Performed: true}, nil
	}
}

func TestAuthenticationHoldsSharedLifecycleLockUntilCompletion(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("per-user lifecycle excludes root")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := privatefs.EnsureDirectory(home); err != nil {
		t.Fatal(err)
	}
	manager, err := service.New(home, os.Geteuid(), &workflowRunner{home: home})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, offlineLeaseKey{}, offlineLease(func(ctx context.Context, run func(context.Context) error) error {
		return manager.WithSession(ctx, func(*service.Session) error { return run(ctx) })
	}))
	c := &blockingAuthentication{entered: make(chan struct{}), release: make(chan struct{})}
	finished := make(chan int, 1)
	go func() {
		finished <- runContext(ctx, []string{"auth", "phone"}, io.Discard, io.Discard, func() (controller, error) { return c, nil }, func() (terminal, error) { return &fakeTerminal{}, nil })
	}()
	select {
	case <-c.entered:
	case <-ctx.Done():
		t.Fatal("authentication never entered")
	}
	contender, stop := context.WithTimeout(ctx, 75*time.Millisecond)
	defer stop()
	err = manager.WithSession(contender, func(*service.Session) error {
		t.Error("relay acquired lifecycle lock during authentication")
		return nil
	})
	if !errors.Is(err, daemon.ErrAccountLocked) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contending start: %v", err)
	}
	close(c.release)
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("authentication returned %d", code)
		}
	case <-ctx.Done():
		t.Fatal("authentication did not finish")
	}
	if err := manager.WithSession(ctx, func(*service.Session) error { return nil }); err != nil {
		t.Fatalf("lifecycle lock retained after authentication: %v", err)
	}
}
