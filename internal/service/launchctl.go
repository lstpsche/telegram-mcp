package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// Runner is the external process boundary. Implementations must discard output.
type Runner interface {
	Run(context.Context, string, ...string) error
}

type processRunner struct{}

func (processRunner) Run(ctx context.Context, program string, args ...string) error {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("service process failed: %w", errors.Join(ctx.Err(), err))
	}
	return nil
}

func (m *Manager) loaded(ctx context.Context) (bool, error) {
	if err := m.runner.Run(ctx, "/bin/launchctl", "print", m.domain()); err != nil {
		return false, fmt.Errorf("inspect login domain: %w", err)
	}
	err := m.runner.Run(ctx, "/bin/launchctl", "print", m.target())
	if err == nil {
		return true, nil
	}
	var status interface{ ExitCode() int }
	if ctx.Err() == nil && errors.As(err, &status) && status.ExitCode() == 113 {
		return false, nil
	}
	return false, fmt.Errorf("inspect service registration: %w", err)
}
