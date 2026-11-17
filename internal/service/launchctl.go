//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func (m *Manager) loaded(ctx context.Context) (bool, error) {
	if err := m.runner.Run(ctx, "/bin/launchctl", "print", m.domain()); err != nil {
		return false, fmt.Errorf("inspect login domain: %w", errors.Join(ErrServiceUnavailable, err))
	}
	err := m.runner.Run(ctx, "/bin/launchctl", "print", m.target())
	if err == nil {
		return true, nil
	}
	var status interface{ ExitCode() int }
	if ctx.Err() == nil && errors.As(err, &status) && status.ExitCode() == 113 {
		return false, nil
	}
	return false, fmt.Errorf("inspect service registration: %w", errors.Join(ErrServiceUnavailable, err))
}

// enabled reads only the documented disabled-service query, not launchctl's
// debug print state. Unsupported output cannot authorize automatic activation.
func (m *Manager) enabled(ctx context.Context) (bool, error) {
	if err := m.runner.Run(ctx, "/bin/launchctl", "print", m.domain()); err != nil {
		return false, errors.Join(ErrServiceUnavailable, err)
	}
	query, ok := m.runner.(Queryer)
	if !ok {
		return false, ErrServiceUnavailable
	}
	data, err := query.Query(ctx, "/bin/launchctl", "print-disabled", m.domain())
	if err != nil {
		return false, errors.Join(ErrServiceUnavailable, err)
	}
	disabled, err := parseDisabledServices(data)
	if err != nil {
		return false, err
	}
	return !disabled, nil
}

func parseDisabledServices(data []byte) (bool, error) {
	if len(data) > maxQueryBytes {
		return false, ErrServiceUnavailable
	}
	output := strings.TrimSpace(string(data))
	if output == "disabled services = (no disabled services)" {
		return false, nil
	}
	lines := strings.Split(output, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "disabled services = {" || strings.TrimSpace(lines[len(lines)-1]) != "}" {
		return false, ErrServiceUnavailable
	}
	seen := map[string]bool{}
	disabled := false
	for _, line := range lines[1 : len(lines)-1] {
		line = strings.TrimSpace(line)
		index := strings.LastIndex(line, " => ")
		if index < 0 {
			return false, ErrServiceUnavailable
		}
		label, err := strconv.Unquote(line[:index])
		value := strings.TrimSpace(line[index+4:])
		if err != nil || label == "" || seen[label] || (value != "enabled" && value != "disabled") {
			return false, ErrServiceUnavailable
		}
		seen[label] = true
		if label == Label {
			disabled = value == "disabled"
		}
	}
	return disabled, nil
}

func (m *Manager) setEnabled(ctx context.Context, enabled bool) error {
	action := "disable"
	if enabled {
		action = "enable"
	}
	if err := m.runner.Run(ctx, "/bin/launchctl", action, m.target()); err != nil {
		return errors.Join(ErrServiceUnavailable, err)
	}
	return nil
}

// A loaded launchd registration need not have a running process. Account-lock
// ownership is checked separately; a non-killing kickstart joins any early start.
func (m *Manager) startupState(ctx context.Context) (bool, error) {
	enabled, err := m.enabled(ctx)
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, ErrDisabled
	}
	return false, nil
}

func (m *Manager) wake(ctx context.Context) error {
	enabled, err := m.enabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrDisabled
	}
	loaded, err := m.loaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		return m.runner.Run(ctx, "/bin/launchctl", "kickstart", m.target())
	}
	return m.runner.Run(ctx, "/bin/launchctl", "bootstrap", m.domain(), m.plistPath())
}
