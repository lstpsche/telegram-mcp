package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/mcpserver"
	"github.com/lstpsche/telegram-mcp/internal/policy"
	"github.com/lstpsche/telegram-mcp/internal/reader"
	"github.com/lstpsche/telegram-mcp/internal/secrets/keychain"
	metastore "github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
	"golang.org/x/sync/errgroup"
)

var (
	ErrConfigurationRequired = errors.New("Telegram account configuration is required")
)

type AuthOutcome struct {
	Performed bool
}

type Status struct {
	Daemon           daemon.SocketState
	Configured       bool
	Environment      string
	TestDC           int
	Authorized       bool
	PhoneCheckPassed bool
	QRCheckPassed    bool
}

// ConfigurationReader obtains application credentials only after Configure
// has established exclusive ownership and confirmed reconfiguration is safe.
type ConfigurationReader func(context.Context) (apiID int, apiHash []byte, err error)

type accountRuntime interface {
	Observe(context.Context, func(context.Context, tgaccount.AuthorizationStatus) error) error
	Authenticate(context.Context, tgaccount.AuthMethod, tgaccount.Prompt, func(context.Context) error) (tgaccount.AuthResult, error)
	Logout(context.Context) error
}

type runtimeFactory func(tgaccount.Config, *tgaccount.KeychainSessionStorage, tgaccount.Mode) (accountRuntime, error)

type Application struct {
	paths     daemon.Paths
	secrets   tgaccount.SecretStore
	factory   runtimeFactory
	now       func() time.Time
	random    io.Reader
	stateMu   sync.RWMutex
	lifecycle *daemon.Lifecycle
}

func New(paths daemon.Paths, secrets tgaccount.SecretStore) (*Application, error) {
	if paths.StateDir == "" || paths.RuntimeDir == "" || paths.Database == "" || paths.Lock == "" || paths.Socket == "" {
		return nil, errors.New("application paths are incomplete")
	}
	if secrets == nil {
		return nil, errors.New("application secret store is required")
	}
	return &Application{
		paths:   paths,
		secrets: secrets,
		factory: func(config tgaccount.Config, storage *tgaccount.KeychainSessionStorage, mode tgaccount.Mode) (accountRuntime, error) {
			return tgaccount.NewAccount(config, storage, mode)
		},
		now:    time.Now,
		random: rand.Reader,
	}, nil
}

func NewDefault() (*Application, error) {
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return nil, err
	}
	secrets, err := keychain.New(tgaccount.KeychainServiceName)
	if err != nil {
		return nil, err
	}
	return New(paths, secrets)
}

// Configure writes non-secret account metadata to SQLite and the atomic
// credential tuple to Keychain. Existing authorization must be logged out first.
func (a *Application) Configure(ctx context.Context, environment string, testDC int, read ConfigurationReader) (resultError error) {
	if a == nil {
		return errors.New("application is not initialized")
	}
	if !tgaccount.ValidEnvironment(environment, testDC) {
		return tgaccount.ErrInvalidConfig
	}
	if read == nil {
		return errors.New("configuration reader is required")
	}
	lock, err := daemon.AcquireAccountLock(a.paths.Lock)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, lock.Release()) }()
	database, repository, err := a.openRepository(ctx)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, database.Close()) }()
	_, authorized, err := repository.Authorization(ctx)
	if err != nil {
		return err
	}
	if authorized {
		return metastore.ErrAuthorizationExists
	}
	// Either environment's surviving session prevents implicit replacement.
	for _, sessionEnvironment := range []string{tgaccount.TestEnvironment, tgaccount.ProductionEnvironment} {
		sessionStorage, err := tgaccount.NewKeychainSessionStorage(a.secrets, sessionEnvironment)
		if err != nil {
			return err
		}
		sessionExists, err := sessionStorage.Exists(ctx)
		if err != nil {
			return fmt.Errorf("inspect Telegram session: %w", err)
		}
		if sessionExists {
			return metastore.ErrAuthorizationExists
		}
	}
	apiID, apiHash, err := read(ctx)
	defer clear(apiHash)
	if err != nil {
		return err
	}
	if err := tgaccount.StoreCredentials(ctx, a.secrets, tgaccount.Config{Environment: environment, APIID: apiID, APIHash: apiHash, TestDC: testDC}); err != nil {
		return fmt.Errorf("store Telegram application credentials: %w", err)
	}
	return repository.SaveConfig(ctx, metastore.AccountConfig{
		APIID:       apiID,
		Environment: environment,
		TestDC:      testDC,
		UpdatedAt:   a.now().UTC(),
	})
}

func (a *Application) Authenticate(
	ctx context.Context,
	method tgaccount.AuthMethod,
	prompt tgaccount.Prompt,
) (outcome AuthOutcome, resultError error) {
	if a == nil {
		return AuthOutcome{}, errors.New("application is not initialized")
	}
	lock, err := daemon.AcquireAccountLock(a.paths.Lock)
	if err != nil {
		return AuthOutcome{}, err
	}
	defer func() { resultError = errors.Join(resultError, lock.Release()) }()
	database, repository, err := a.openRepository(ctx)
	if err != nil {
		return AuthOutcome{}, err
	}
	defer func() { resultError = errors.Join(resultError, database.Close()) }()
	mode := tgaccount.ModeNoUpdates
	if method == tgaccount.AuthMethodQR {
		mode = tgaccount.ModeQRAuth
	}
	config, account, err := a.account(ctx, repository, mode)
	if err != nil {
		return AuthOutcome{}, err
	}
	result, err := account.Authenticate(ctx, method, prompt, repository.InvalidateAuthorization)
	if err != nil {
		return AuthOutcome{}, err
	}
	_, hasCurrent, err := repository.Authorization(ctx)
	if err != nil {
		return AuthOutcome{}, err
	}
	if !result.Performed && hasCurrent {
		return AuthOutcome{}, nil
	}
	epoch, err := newEpoch(a.random)
	if err != nil {
		return AuthOutcome{}, err
	}
	var checkedMethod *metastore.AuthMethod
	if result.Performed {
		converted := storeAuthMethod(method)
		checkedMethod = &converted
	}
	if err := repository.RecordAuthorization(ctx, epoch, checkedMethod, config.TestDC, a.now().UTC()); err != nil {
		return AuthOutcome{}, err
	}
	return AuthOutcome{Performed: result.Performed}, nil
}

func (a *Application) Logout(ctx context.Context) (resultError error) {
	if a == nil {
		return errors.New("application is not initialized")
	}
	lock, err := daemon.AcquireAccountLock(a.paths.Lock)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, lock.Release()) }()
	database, repository, err := a.openRepository(ctx)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, database.Close()) }()
	config, account, err := a.account(ctx, repository, tgaccount.ModeNoUpdates)
	if err != nil {
		return err
	}
	if err := account.Logout(ctx); err != nil {
		return err
	}
	sessionStorage, err := tgaccount.NewKeychainSessionStorage(a.secrets, config.Environment)
	if err != nil {
		return err
	}
	if err := sessionStorage.Delete(ctx); err != nil {
		return fmt.Errorf("delete Telegram session: %w", err)
	}
	return repository.InvalidateAuthorization(ctx)
}

func (a *Application) Status(ctx context.Context) (status Status, resultError error) {
	if a == nil {
		return Status{}, errors.New("application is not initialized")
	}
	socketState, err := daemon.ProbeSocket(a.paths.Socket)
	if err != nil {
		return Status{}, err
	}
	status = Status{Daemon: socketState}
	if _, err := os.Lstat(a.paths.Database); errors.Is(err, os.ErrNotExist) {
		return status, nil
	} else if err != nil {
		return Status{}, fmt.Errorf("inspect account metadata: %w", err)
	}
	database, repository, err := a.openRepository(ctx)
	if err != nil {
		return Status{}, err
	}
	defer func() { resultError = errors.Join(resultError, database.Close()) }()
	metadataStatus, err := repository.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Configured = metadataStatus.Configured
	status.Environment = metadataStatus.Config.Environment
	status.TestDC = metadataStatus.Config.TestDC
	status.Authorized = metadataStatus.Authorized
	status.PhoneCheckPassed = metadataStatus.PhoneCheck
	status.QRCheckPassed = metadataStatus.QRCheck
	return status, nil
}

func (a *Application) RunDaemon(ctx context.Context) (runError error) {
	if a == nil {
		return errors.New("application is not initialized")
	}
	lifecycle, err := daemon.NewLifecycle(a.now)
	if err != nil {
		return err
	}
	a.stateMu.Lock()
	a.lifecycle = lifecycle
	a.stateMu.Unlock()
	var lock *daemon.AccountLock
	var database *sql.DB
	var socket *daemon.Socket
	normalStop := false
	defer func() {
		if normalStop {
			if err := lifecycle.Transition(daemon.StateStopping); err != nil {
				runError = errors.Join(runError, err)
				normalStop = false
			}
		}
		var cleanupError error
		if socket != nil {
			cleanupError = errors.Join(cleanupError, socket.Close())
		}
		if database != nil {
			cleanupError = errors.Join(cleanupError, database.Close())
		}
		if lock != nil {
			cleanupError = errors.Join(cleanupError, lock.Release())
		}
		if cleanupError != nil {
			runError = errors.Join(runError, cleanupError)
			if lifecycle.Snapshot().State != daemon.StateFailed {
				_ = lifecycle.Transition(daemon.StateFailed)
			}
			return
		}
		if normalStop {
			if err := lifecycle.Transition(daemon.StateStopped); err != nil {
				runError = errors.Join(runError, err)
			}
		}
	}()

	lock, err = daemon.AcquireAccountLock(a.paths.Lock)
	if err != nil {
		_ = lifecycle.Transition(daemon.StateFailed)
		return err
	}
	if err := lifecycle.Transition(daemon.StateLocked); err != nil {
		_ = lifecycle.Transition(daemon.StateFailed)
		return err
	}

	var repository *metastore.Repository
	database, repository, err = a.openRepository(ctx)
	if err != nil {
		_ = lifecycle.Transition(daemon.StateFailed)
		return err
	}
	epochState, authorized, err := repository.Authorization(ctx)
	if err != nil {
		_ = lifecycle.Transition(daemon.StateFailed)
		return err
	}
	var account accountRuntime
	var textService *reader.Service
	if authorized {
		_, account, err = a.account(ctx, repository, tgaccount.ModeRead)
		if err != nil && !errors.Is(err, ErrConfigurationRequired) {
			_ = lifecycle.Transition(daemon.StateFailed)
			return err
		}
		if account != nil {
			backend, ok := account.(interface {
				reader.Backend
				EnableReads(context.Context, *sql.DB, string) error
			})
			if !ok {
				_ = lifecycle.Transition(daemon.StateFailed)
				return errors.New("account text runtime is unavailable")
			}
			if err := backend.EnableReads(ctx, database, epochState.Epoch); err != nil {
				_ = lifecycle.Transition(daemon.StateFailed)
				return err
			}
			policies, err := policy.New(database, filepath.Join(a.paths.StateDir, "policy.lock"), a.now)
			if err != nil {
				_ = lifecycle.Transition(daemon.StateFailed)
				return err
			}
			cursorKey, keyErr := a.cursorKey(ctx)
			if keyErr != nil {
				_ = lifecycle.Transition(daemon.StateFailed)
				return keyErr
			}
			textService, err = reader.New(backend, policies, a.now, cursorKey)
			clear(cursorKey)
			if err != nil {
				_ = lifecycle.Transition(daemon.StateFailed)
				return err
			}
		}
	}
	if account == nil {
		_ = lifecycle.Transition(daemon.StateReauthRequired)
	}

	socket, err = daemon.BindSocket(a.paths.Socket)
	if err != nil {
		_ = lifecycle.Transition(daemon.StateFailed)
		return err
	}
	server := mcpserver.New(lifecycle.Snapshot, textService)
	if account == nil {
		err := socket.Serve(ctx, func(ctx context.Context, conn *net.UnixConn) { mcpserver.Serve(ctx, server, conn) })
		if ctx.Err() != nil {
			normalStop = true
			return nil
		}
		_ = lifecycle.Transition(daemon.StateFailed)
		return err
	}
	if err := lifecycle.Transition(daemon.StateConnecting); err != nil {
		_ = lifecycle.Transition(daemon.StateFailed)
		return err
	}

	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		return socket.Serve(groupContext, func(ctx context.Context, conn *net.UnixConn) { mcpserver.Serve(ctx, server, conn) })
	})
	group.Go(func() error {
		return account.Observe(groupContext, func(observeContext context.Context, authorization tgaccount.AuthorizationStatus) error {
			if !authorization.Authorized {
				if err := repository.InvalidateAuthorization(observeContext); err != nil {
					return err
				}
				if err := lifecycle.Transition(daemon.StateReauthRequired); err != nil {
					return err
				}
				<-observeContext.Done()
				return observeContext.Err()
			}
			if current, exists, err := repository.Authorization(observeContext); err != nil {
				return err
			} else if !exists || current.Epoch != epochState.Epoch {
				return tgaccount.ErrReauthenticationRequired
			}
			if err := lifecycle.Transition(daemon.StateReady); err != nil {
				return err
			}
			<-observeContext.Done()
			return observeContext.Err()
		})
	})
	err = group.Wait()
	if errors.Is(err, tgaccount.ErrReauthenticationRequired) {
		if invalidateError := repository.InvalidateAuthorization(ctx); invalidateError != nil {
			_ = lifecycle.Transition(daemon.StateFailed)
			return errors.Join(err, invalidateError)
		}
		if lifecycle.Snapshot().State != daemon.StateReauthRequired {
			if transitionError := lifecycle.Transition(daemon.StateReauthRequired); transitionError != nil {
				_ = lifecycle.Transition(daemon.StateFailed)
				return errors.Join(err, transitionError)
			}
		}
		return err
	}
	if ctx.Err() != nil && (err == nil || errors.Is(err, ctx.Err())) {
		normalStop = true
		return nil
	}
	_ = lifecycle.Transition(daemon.StateFailed)
	if err == nil {
		return errors.New("daemon runtime exited unexpectedly")
	}
	return err
}

func (a *Application) Lifecycle() daemon.Snapshot {
	if a == nil {
		return daemon.Snapshot{}
	}
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if a.lifecycle == nil {
		return daemon.Snapshot{}
	}
	return a.lifecycle.Snapshot()
}

func (a *Application) openRepository(ctx context.Context) (*sql.DB, *metastore.Repository, error) {
	database, err := metastore.Open(ctx, a.paths.Database)
	if err != nil {
		return nil, nil, err
	}
	repository, err := metastore.NewRepository(database)
	if err != nil {
		_ = database.Close()
		return nil, nil, err
	}
	return database, repository, nil
}

func (a *Application) account(
	ctx context.Context,
	repository *metastore.Repository,
	mode tgaccount.Mode,
) (metastore.AccountConfig, accountRuntime, error) {
	config, configured, err := repository.Config(ctx)
	if err != nil {
		return metastore.AccountConfig{}, nil, err
	}
	if !configured {
		return metastore.AccountConfig{}, nil, ErrConfigurationRequired
	}
	credentials, err := tgaccount.LoadCredentials(ctx, a.secrets)
	if errors.Is(err, keychain.ErrNotFound) {
		clear(credentials.APIHash)
		return metastore.AccountConfig{}, nil, ErrConfigurationRequired
	}
	if err != nil {
		clear(credentials.APIHash)
		return metastore.AccountConfig{}, nil, err
	}
	defer clear(credentials.APIHash)
	if credentials.APIID != config.APIID || credentials.TestDC != config.TestDC || credentials.Environment != config.Environment {
		return metastore.AccountConfig{}, nil, ErrConfigurationRequired
	}
	sessionStorage, err := tgaccount.NewKeychainSessionStorage(a.secrets, config.Environment)
	if err != nil {
		return metastore.AccountConfig{}, nil, err
	}
	account, err := a.factory(credentials, sessionStorage, mode)
	if err != nil {
		return metastore.AccountConfig{}, nil, err
	}
	return config, account, nil
}

func newEpoch(random io.Reader) (string, error) {
	if random == nil {
		return "", errors.New("authorization epoch random source is required")
	}
	value := make([]byte, 32)
	if _, err := io.ReadFull(random, value); err != nil {
		clear(value)
		return "", errors.New("generate authorization epoch")
	}
	epoch := base64.RawURLEncoding.EncodeToString(value)
	clear(value)
	return epoch, nil
}

func storeAuthMethod(method tgaccount.AuthMethod) metastore.AuthMethod {
	if method == tgaccount.AuthMethodQR {
		return metastore.AuthMethodQR
	}
	return metastore.AuthMethodPhone
}
