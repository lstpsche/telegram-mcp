package control

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/distribution"
	"github.com/lstpsche/telegram-mcp/internal/service"
)

type lifecycleService interface {
	serviceManager
	Enabled(context.Context) (bool, error)
	SetEnabled(context.Context, bool) error
	Quiesce(context.Context) error
	InstallWithState(context.Context, string, bool) (service.Config, error)
}

type sessionProvider interface {
	withControlSession(context.Context, func(lifecycleService) error) error
}

type managedService struct{ *service.Manager }

func (m managedService) withControlSession(ctx context.Context, run func(lifecycleService) error) error {
	return m.WithSession(ctx, func(session *service.Session) error { return run(session) })
}

func (s support) withSession(ctx context.Context, run func(lifecycleService) error) error {
	provider, ok := s.service.(sessionProvider)
	if !ok {
		return errors.New("service lifecycle session is unavailable")
	}
	return provider.withControlSession(ctx, run)
}

func (s support) inSession(manager lifecycleService) support {
	return support{service: manager, inspect: s.inspect}
}

type offlineLease func(context.Context, func(context.Context) error) error
type offlineLeaseKey struct{}

func defaultOfflineLease(ctx context.Context, run func(context.Context) error) error {
	manager, err := service.Default()
	if err != nil {
		return err
	}
	return manager.WithSession(ctx, func(_ *service.Session) error { return run(ctx) })
}

// The public CLI entry supplies coordination. Direct handler composition can
// already own the enclosing lifecycle session, as the setup guides do.
func withOfflineLease(ctx context.Context, run func(context.Context) error) error {
	if lease, ok := ctx.Value(offlineLeaseKey{}).(offlineLease); ok {
		return lease(ctx, run)
	}
	return run(ctx)
}

// Setup uses the same managed activation lock as upgrades before taking the
// service lease. Manual installations have no shared stable entry to replace.
func prepareSetupEntry(ctx context.Context, directory string, run func() error) (result error) {
	root, err := distribution.DefaultRoot()
	if err != nil {
		return err
	}
	version := distribution.ManagedVersion(root, directory)
	if version == "" {
		return run()
	}
	lock, err := daemon.AcquireAccountLock(filepath.Join(root, "upgrade.lock"))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Release()) }()
	if err := verifyReleasePrograms(ctx, directory, version, runClientCommand); err != nil {
		return err
	}
	if err := distribution.EnsureEntry(ctx, directory); err != nil {
		return err
	}
	return run()
}
