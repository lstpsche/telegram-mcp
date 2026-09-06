//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
)

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
