package telegram

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	gotdtelegram "github.com/gotd/td/telegram"
	gotdauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"golang.org/x/time/rate"
)

const (
	maximumConcurrentRequests = 4
	requestsPerSecond         = 5
	requestBurst              = 2
	maximumFloodWait          = 30 * time.Second
	maximumFloodRetries       = 3
)

var (
	ErrInvalidConfig            = errors.New("Telegram Test-DC configuration is invalid")
	ErrQRModeRequired           = errors.New("QR authentication requires a QR-enabled runtime")
	ErrAuthenticationRejected   = errors.New("Telegram authentication was rejected")
	ErrReauthenticationRequired = errors.New("Telegram authorization is no longer valid")
	ErrTelegramUnavailable      = errors.New("Telegram operation is unavailable")
)

type Config struct {
	APIID   int
	APIHash []byte
	TestDC  int
}

type Mode uint8

const (
	ModeNoUpdates Mode = iota + 1
	ModeQRAuth
)

type Account struct {
	client   *gotdtelegram.Client
	waiter   *floodwait.Waiter
	loggedIn qrlogin.LoggedIn
}

// NewAccount builds a gotd client pinned to Telegram Test DCs. There is no
// production-DC construction path in Phase 1.
func NewAccount(config Config, storage gotdtelegram.SessionStorage, mode Mode) (*Account, error) {
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	if storage == nil {
		return nil, errors.New("Telegram session storage is required")
	}
	if mode != ModeNoUpdates && mode != ModeQRAuth {
		return nil, errors.New("Telegram account mode is invalid")
	}

	waiter := floodwait.NewWaiter().
		WithMaxWait(maximumFloodWait).
		WithMaxRetries(maximumFloodRetries)
	options := gotdtelegram.Options{
		DC:               config.TestDC,
		DCList:           dcs.Test(),
		NoUpdates:        true,
		SessionStorage:   storage,
		DialTimeout:      10 * time.Second,
		ExchangeTimeout:  15 * time.Second,
		MigrationTimeout: 15 * time.Second,
		MaxRetries:       3,
		Middlewares: []gotdtelegram.Middleware{
			newConcurrencyLimiter(maximumConcurrentRequests),
			waiter,
			ratelimit.New(rate.Limit(requestsPerSecond), requestBurst),
		},
	}
	var loggedIn qrlogin.LoggedIn
	if mode == ModeQRAuth {
		dispatcher := tg.NewUpdateDispatcher()
		loggedIn = qrlogin.OnLoginToken(dispatcher)
		options.NoUpdates = false
		options.UpdateHandler = dispatcher
	}
	client := gotdtelegram.NewClient(config.APIID, string(config.APIHash), options)
	return &Account{client: client, waiter: waiter, loggedIn: loggedIn}, nil
}

type AuthorizationStatus struct {
	Authorized bool
}

// Observe runs the account and passes a content-free authorization status to
// the daemon callback. The callback normally blocks until cancellation.
func (a *Account) Observe(ctx context.Context, callback func(context.Context, AuthorizationStatus) error) error {
	if callback == nil {
		return errors.New("authorization status callback is required")
	}
	var callbackError error
	err := a.run(ctx, func(runContext context.Context) error {
		status, err := a.client.Auth().Status(runContext)
		if err != nil {
			return sanitizeRuntimeError(err)
		}
		callbackError = callback(runContext, AuthorizationStatus{Authorized: status.Authorized})
		return callbackError
	})
	if gotdauth.IsUnauthorized(err) {
		return ErrReauthenticationRequired
	}
	if callbackError != nil && errors.Is(err, callbackError) {
		return callbackError
	}
	return sanitizeAccountError(err)
}

func (a *Account) run(ctx context.Context, callback func(context.Context) error) error {
	if a == nil || a.client == nil || a.waiter == nil {
		return errors.New("Telegram account is not initialized")
	}
	return a.waiter.Run(ctx, func(waitContext context.Context) error {
		return a.client.Run(waitContext, callback)
	})
}

var apiHashPattern = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)

func ValidateConfig(config Config) error {
	if config.APIID <= 0 || int64(config.APIID) > int64(1<<31-1) ||
		config.TestDC < 1 || config.TestDC > 3 || !apiHashPattern.Match(config.APIHash) {
		return ErrInvalidConfig
	}
	return nil
}

func sanitizeRuntimeError(err error) error {
	if gotdauth.IsUnauthorized(err) {
		return ErrReauthenticationRequired
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrTelegramUnavailable
}

func sanitizeAccountError(err error) error {
	if err == nil {
		return err
	}
	if gotdauth.IsUnauthorized(err) {
		return ErrReauthenticationRequired
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrAuthenticationRejected) || errors.Is(err, ErrQRModeRequired) ||
		errors.Is(err, ErrReauthenticationRequired) || errors.Is(err, ErrTelegramUnavailable) {
		return err
	}
	return ErrTelegramUnavailable
}
