package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/logging"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

const startupDeadlineEnvironment = "TELEGRAM_MCP_STARTUP_DEADLINE_UNIX_MS"
const startupTimeout = 8 * time.Second

type startupFailure struct {
	reason logging.StartupReason
	cause  error
}

func (e *startupFailure) Error() string { return "relay startup failed" }
func (e *startupFailure) Unwrap() error { return e.cause }

func startupDeadline() (time.Time, error) {
	now := time.Now()
	deadline := now.Add(startupTimeout)
	executable, err := os.Executable()
	if err != nil {
		return time.Time{}, err
	}
	root, err := distribution.DefaultRoot()
	if err != nil {
		return time.Time{}, err
	}
	// A stable entry creates the budget; only its selected child inherits it.
	stable, err := isManagedEntry(executable, root)
	if err != nil {
		return time.Time{}, err
	}
	if stable {
		return deadline, nil
	}
	if value, present := os.LookupEnv(startupDeadlineEnvironment); present {
		millis, err := strconv.ParseInt(value, 10, 64)
		if err != nil || millis <= 0 || strconv.FormatInt(millis, 10) != value {
			return time.Time{}, &startupFailure{logging.StartupStateInvalid, errors.New("invalid startup deadline")}
		}
		inherited := time.UnixMilli(millis)
		if inherited.Before(deadline) {
			deadline = inherited
		}
	}
	return deadline, nil
}

type startupSession interface {
	RequestStart(context.Context, time.Time) (service.StartDisposition, error)
}

type startupLease func(context.Context, func(startupSession) error) error
type endpointDial func(context.Context, string) (net.Conn, error)

func connectDaemon(ctx context.Context, paths daemon.Paths) (net.Conn, error) {
	return connectWithStartup(ctx, paths.Socket, daemon.DialSocket, func(ctx context.Context, action func(startupSession) error) error {
		manager, err := service.Default()
		if err != nil {
			return err
		}
		// Prove a human installation exists before creating a lifecycle lock.
		// RequestStart revalidates it after acquiring that lock.
		installed, err := manager.Inspect(ctx)
		if err != nil {
			return err
		}
		if installed == nil {
			return service.ErrNotInstalled
		}
		return manager.WithSession(ctx, func(session *service.Session) error { return action(session) })
	})
}

func connectWithStartup(ctx context.Context, path string, dial endpointDial, lease startupLease) (net.Conn, error) {
	connection, err := dial(ctx, path)
	if err == nil {
		return connection, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !endpointUnavailable(err) {
		return nil, &startupFailure{logging.StartupUnsafeEndpoint, err}
	}
	entered := false
	err = lease(ctx, func(session startupSession) error {
		entered = true
		connection, err = dial(ctx, path)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !endpointUnavailable(err) {
			return &startupFailure{logging.StartupUnsafeEndpoint, err}
		}
		if _, err := session.RequestStart(ctx, time.Now()); err != nil {
			return err
		}
		timer := time.NewTicker(50 * time.Millisecond)
		defer timer.Stop()
		for {
			connection, err = dial(ctx, path)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !endpointUnavailable(err) {
				return &startupFailure{logging.StartupUnsafeEndpoint, err}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	})
	if err != nil {
		if connection != nil {
			err = errors.Join(err, connection.Close())
		}
		if !entered && errors.Is(err, context.DeadlineExceeded) {
			return nil, &startupFailure{logging.StartupMaintenanceBusy, err}
		}
		return nil, err
	}
	return connection, nil
}

func endpointUnavailable(err error) bool {
	return errors.Is(err, daemon.ErrDaemonUnavailable) &&
		!errors.Is(err, daemon.ErrUnsafeSocket) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

func startupReason(err error) logging.StartupReason {
	var failure *startupFailure
	if errors.As(err, &failure) {
		return failure.reason
	}
	switch {
	case errors.Is(err, context.Canceled):
		return logging.StartupCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return logging.StartupTimeout
	case errors.Is(err, service.ErrNotInstalled):
		return logging.StartupNotInstalled
	case errors.Is(err, service.ErrDisabled):
		return logging.StartupDisabled
	case errors.Is(err, service.ErrStartupState):
		return logging.StartupStateInvalid
	case errors.Is(err, service.ErrInvalidInstallation), errors.Is(err, service.ErrUnsafePath),
		errors.Is(err, distribution.ErrEntryInvalid), errors.Is(err, distribution.ErrEntryMigrationRequired):
		return logging.StartupInvalidInstallation
	case errors.Is(err, daemon.ErrUnsafeSocket):
		return logging.StartupUnsafeEndpoint
	case errors.Is(err, service.ErrServiceUnavailable):
		return logging.StartupServiceUnavailable
	default:
		return logging.StartupFailed
	}
}
