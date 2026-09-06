package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Runner is the external process boundary. Implementations must discard output.
type Runner interface {
	Run(context.Context, string, ...string) error
}

type processRunner struct{}

func (processRunner) Run(ctx context.Context, program string, args ...string) error {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("service process failed: %w", errors.Join(ctx.Err(), err))
	}
	return nil
}
