package service

import (
	"bytes"
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

// Queryer exposes bounded machine-readable operating-system state.
// Implementations discard stderr and never include captured output in errors.
type Queryer interface {
	Query(context.Context, string, ...string) ([]byte, error)
}

const maxQueryBytes = 64 * 1024

type queryOutput struct{ bytes.Buffer }

func (b *queryOutput) Write(p []byte) (int, error) {
	if len(p) > maxQueryBytes-b.Len() {
		return 0, ErrServiceUnavailable
	}
	return b.Buffer.Write(p)
}

func (processRunner) Query(ctx context.Context, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = os.Environ()
	output := &queryOutput{}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("service query failed: %w", errors.Join(ErrServiceUnavailable, ctx.Err(), err))
	}
	return output.Bytes(), nil
}
