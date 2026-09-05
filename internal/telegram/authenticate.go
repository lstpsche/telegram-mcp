package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/awnumar/memguard"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/telegram/auth/srpguard"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const (
	maximumPhoneBytes    = 32
	maximumCodeBytes     = 32
	maximumPasswordBytes = 1024
	maximumPasswordTries = 3
)

type AuthMethod string

const (
	AuthMethodPhone AuthMethod = "phone"
	AuthMethodQR    AuthMethod = "qr"
)

type Prompt interface {
	Phone(ctx context.Context) ([]byte, error)
	Code(ctx context.Context) ([]byte, error)
	Password(ctx context.Context) ([]byte, error)
	ShowQRCode(ctx context.Context, tokenURL string, expiresAt time.Time) error
}

type AuthResult struct {
	Performed bool
}

// Authenticate performs one explicit login. onUnauthorized runs after
// Telegram proves the existing session is unauthorized and before prompting.
func (a *Account) Authenticate(
	ctx context.Context,
	method AuthMethod,
	prompt Prompt,
	onUnauthorized func(context.Context) error,
) (AuthResult, error) {
	if prompt == nil {
		return AuthResult{}, errors.New("interactive authentication prompt is required")
	}
	if method != AuthMethodPhone && method != AuthMethodQR {
		return AuthResult{}, errors.New("authentication method is invalid")
	}
	if method == AuthMethodQR && (a == nil || a.loggedIn == nil) {
		return AuthResult{}, ErrQRModeRequired
	}
	var result AuthResult
	err := a.run(ctx, func(runContext context.Context) error {
		status, err := a.client.Auth().Status(runContext)
		if err != nil {
			return sanitizeRuntimeError(err)
		}
		if status.Authorized {
			return nil
		}
		if onUnauthorized != nil {
			if err := onUnauthorized(runContext); err != nil {
				return fmt.Errorf("invalidate previous authorization: %w", err)
			}
		}
		switch method {
		case AuthMethodPhone:
			err = auth.NewFlow(interactiveAuthenticator{prompt: prompt}, auth.SendCodeOptions{}).Run(runContext, a.client.Auth())
		case AuthMethodQR:
			err = a.authenticateQR(runContext, prompt)
		}
		if err != nil {
			return sanitizeAuthenticationError(err)
		}
		status, err = a.client.Auth().Status(runContext)
		if err != nil {
			return sanitizeRuntimeError(err)
		}
		if !status.Authorized {
			return ErrAuthenticationRejected
		}
		result.Performed = true
		return nil
	})
	return result, sanitizeAccountError(err)
}

func (a *Account) authenticateQR(ctx context.Context, prompt Prompt) error {
	_, err := a.client.QR().Auth(ctx, a.loggedIn, func(showContext context.Context, token qrlogin.Token) error {
		return prompt.ShowQRCode(showContext, token.URL(), token.Expires())
	})
	if err == nil {
		return nil
	}
	if !tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
		return err
	}
	for attempt := 0; attempt < maximumPasswordTries; attempt++ {
		secret, promptError := prompt.Password(ctx)
		if promptError != nil {
			clear(secret)
			return promptError
		}
		if err := validatePassword(secret); err != nil {
			clear(secret)
			return err
		}
		buffer := memguard.NewBufferFromBytes(secret)
		clear(secret)
		if !buffer.IsAlive() || buffer.Size() == 0 {
			buffer.Destroy()
			return errors.New("protect 2FA password in memory")
		}
		_, passwordError := a.client.Auth().PasswordWith(ctx, srpguard.LockedBuffer(buffer))
		if buffer.IsAlive() {
			buffer.Destroy()
		}
		if passwordError == nil {
			return nil
		}
		if !errors.Is(passwordError, auth.ErrPasswordInvalid) {
			return passwordError
		}
	}
	return ErrAuthenticationRejected
}

type interactiveAuthenticator struct {
	prompt Prompt
}

func (a interactiveAuthenticator) Phone(ctx context.Context) (string, error) {
	secret, err := a.prompt.Phone(ctx)
	if err != nil {
		clear(secret)
		return "", err
	}
	defer clear(secret)
	if len(secret) < 5 || len(secret) > maximumPhoneBytes {
		return "", ErrAuthenticationRejected
	}
	for index, character := range secret {
		if character >= '0' && character <= '9' {
			continue
		}
		if index == 0 && character == '+' {
			continue
		}
		return "", ErrAuthenticationRejected
	}
	return string(secret), nil
}

func (a interactiveAuthenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	secret, err := a.prompt.Code(ctx)
	if err != nil {
		clear(secret)
		return "", err
	}
	defer clear(secret)
	if len(secret) == 0 || len(secret) > maximumCodeBytes {
		return "", ErrAuthenticationRejected
	}
	for _, character := range secret {
		if character < '0' || character > '9' {
			return "", ErrAuthenticationRejected
		}
	}
	return string(secret), nil
}

func (a interactiveAuthenticator) Password(context.Context) (string, error) {
	return "", errors.New("plaintext password path is disabled")
}

func (a interactiveAuthenticator) PasswordHash(ctx context.Context, parameters *tg.AccountPassword) (*tg.InputCheckPasswordSRP, error) {
	secret, err := a.prompt.Password(ctx)
	if err != nil {
		clear(secret)
		return nil, err
	}
	if err := validatePassword(secret); err != nil {
		clear(secret)
		return nil, err
	}
	buffer := memguard.NewBufferFromBytes(secret)
	clear(secret)
	if !buffer.IsAlive() || buffer.Size() == 0 {
		buffer.Destroy()
		return nil, errors.New("protect 2FA password in memory")
	}
	hash, err := srpguard.LockedBuffer(buffer)(ctx, parameters)
	if buffer.IsAlive() {
		buffer.Destroy()
	}
	return hash, err
}

func (a interactiveAuthenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("new-account sign-up is not supported")
}

func (a interactiveAuthenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("new-account sign-up is not supported")
}

func validatePassword(secret []byte) error {
	if len(secret) == 0 {
		return ErrPasswordRequired
	}
	if len(secret) > maximumPasswordBytes || !utf8.Valid(secret) || bytes.IndexByte(secret, 0) >= 0 {
		return ErrAuthenticationRejected
	}
	return nil
}

func sanitizeAuthenticationError(err error) error {
	if errors.Is(err, ErrPasswordRequired) {
		return ErrPasswordRequired
	}
	if tgerr.Is(err, "FLOOD_WAIT", "FLOOD_PREMIUM_WAIT", "PHONE_NUMBER_FLOOD", "PHONE_PASSWORD_FLOOD") {
		return ErrAuthenticationRateLimited
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrAuthenticationRejected) || errors.Is(err, auth.ErrPasswordInvalid) || tgerr.Is(err,
		"PHONE_CODE_INVALID",
		"PHONE_CODE_EXPIRED",
		"PHONE_NUMBER_INVALID",
		"PASSWORD_HASH_INVALID",
	) {
		return ErrAuthenticationRejected
	}
	return ErrTelegramUnavailable
}
