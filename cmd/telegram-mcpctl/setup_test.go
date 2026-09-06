package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/diagnostics"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

type setupAnswers struct {
	fakeTerminal
	answers []string
}

func (p *setupAnswers) Ask(ctx context.Context, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(p.answers) == 0 {
		return "", io.EOF
	}
	answer := p.answers[0]
	p.answers = p.answers[1:]
	return answer, nil
}

type setupService struct {
	fakeService
	actions []string
}

func (f *setupService) Install(_ context.Context, dir string) (service.Config, error) {
	f.actions = append(f.actions, "install")
	f.installed = &service.Config{BinDir: dir, Relay: dir + "/telegram-mcp"}
	return *f.installed, f.err
}
func (f *setupService) Start(context.Context) error {
	f.actions = append(f.actions, "start")
	return f.err
}
func (f *setupService) Stop(context.Context) error {
	f.actions = append(f.actions, "stop")
	return f.err
}

func TestSetupFreshAndResumePreserveAuthority(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "resume"}[resume], func(t *testing.T) {
			control := &fakeAccessController{}
			answers := []string{"test2", "phone", "", "json"}
			if resume {
				control.status = app.Status{Configured: true, Authorized: true}
				control.authError = errors.New("must not reauthenticate")
				answers = []string{"", "json"}
			}
			prompt := &setupAnswers{fakeTerminal: fakeTerminal{apiID: 12, apiHash: []byte("secret")}, answers: answers}
			manager := &setupService{}
			calls := 0
			ready := true
			var output bytes.Buffer
			s := setupSession{control: control, prompt: prompt, output: &output, binDir: "/private/bin", local: support{service: manager, inspect: func(context.Context) (diagnostics.Report, error) {
				calls++
				if calls == 1 {
					return diagnostics.Report{}, nil
				}
				return diagnostics.Report{MCP: true, MessageReads: &ready}, nil
			}}}
			if err := s.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if control.writes != 0 || control.full || strings.Contains(output.String(), "secret") {
				t.Fatal("setup changed or disclosed authority")
			}
			if !reflect.DeepEqual(manager.actions, []string{"install", "start"}) {
				t.Fatal(manager.actions)
			}
			if resume && control.configuredAPIID != 0 {
				t.Fatal("resume reconfigured")
			}
			if !resume && control.configuredDC != 2 {
				t.Fatal("wrong environment")
			}
		})
	}
}

func TestSetupRefusalDoesNotConfigureOrEnableFullRead(t *testing.T) {
	control := &fakeAccessController{}
	s := setupSession{control: control, prompt: &setupAnswers{answers: []string{"production", "no"}}, output: io.Discard, local: support{service: &setupService{}, inspect: func(context.Context) (diagnostics.Report, error) { return diagnostics.Report{}, nil }}}
	if err := s.run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if control.configuredAPIID != 0 || control.writes != 0 {
		t.Fatal("refusal mutated account")
	}
	s.prompt = &setupAnswers{answers: []string{"full", "no"}}
	if err := s.chooseAccess(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if control.writes != 0 {
		t.Fatal("refusal enabled access")
	}
}

func TestSetupDoesNotConnectBeforeReadiness(t *testing.T) {
	control := &fakeAccessController{}
	control.status = app.Status{Configured: true, Authorized: true}
	prompt := &setupAnswers{answers: []string{"", "codex"}}
	count := 0
	failure := errors.New("probe failure")
	s := setupSession{control: control, prompt: prompt, output: io.Discard, local: support{service: &setupService{}, inspect: func(context.Context) (diagnostics.Report, error) {
		count++
		if count > 1 {
			return diagnostics.Report{}, failure
		}
		return diagnostics.Report{}, nil
	}}}
	if err := s.run(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prompt.answers, []string{"codex"}) {
		t.Fatal("connected before readiness")
	}
}

func TestSetupClientConflictAndExplicitRegistration(t *testing.T) {
	for _, conflict := range []bool{true, false} {
		calls := 0
		s := setupSession{prompt: &setupAnswers{answers: []string{"codex", "yes"}}, output: io.Discard, command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls++
			if name != "codex" {
				t.Fatal(name)
			}
			if calls == 1 {
				if conflict {
					return []byte(`[{"name":"telegram"}]`), nil
				}
				return []byte(`[]`), nil
			}
			if !reflect.DeepEqual(args, []string{"mcp", "add", "telegram", "--", "/private/relay"}) {
				t.Fatal(args)
			}
			return nil, nil
		}}
		err := s.connectClient(context.Background(), "/private/relay")
		if conflict && (err == nil || calls != 1) {
			t.Fatal("existing registration changed")
		}
		if !conflict && (err != nil || calls != 2) {
			t.Fatal(err, calls)
		}
	}
}
