package telegram

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestExpiredFloodWaitDoesNotDelayFreshRequests(t *testing.T) {
	account, err := NewAccount(Config{Environment: TestEnvironment, APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef"), TestDC: 2}, &session.StorageMemory{}, ModeRead)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	invoke := account.middlewares[1].Handle(gotdtelegram.InvokeFunc(func(ctx context.Context, _ bin.Encoder, _ bin.Decoder) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if calls.Add(1) == 1 {
			return tgerr.New(420, "FLOOD_WAIT_2")
		}
		return nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, cancelFirst := context.WithTimeout(ctx, 100*time.Millisecond)
	err = invoke.Invoke(first, &tg.MessagesGetDialogsRequest{}, nil)
	cancelFirst()
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatal("first request did not reach its flood wait deadline", err, calls.Load())
	}
	// Let the server's requested wait fully elapse after cancellation.
	select {
	case <-time.After(2100 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	fresh, cancelFresh := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancelFresh()
	if err := invoke.Invoke(fresh, &tg.MessagesGetDialogsRequest{}, nil); err != nil {
		t.Fatal("fresh request retained an expired flood wait", err)
	}
	if calls.Load() != 2 {
		t.Fatal("fresh request did not reach the upstream invoker", calls.Load())
	}
}

func TestFloodWaitKeepsWaitAndRetryBounds(t *testing.T) {
	for _, mode := range []string{"wait_limit", "retry_limit"} {
		t.Run(mode, func(t *testing.T) {
			account, err := NewAccount(Config{Environment: TestEnvironment, APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef"), TestDC: 2}, &session.StorageMemory{}, ModeNoUpdates)
			if err != nil {
				t.Fatal(err)
			}
			failure := tgerr.New(420, "FLOOD_WAIT_31")
			expectedCalls := 1
			if mode == "retry_limit" {
				failure = tgerr.New(420, "FLOOD_WAIT_1")
				expectedCalls = maximumFloodRetries + 1
			}
			calls := 0
			invoke := account.middlewares[1].Handle(gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				calls++
				return failure
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			err = invoke.Invoke(ctx, &tg.MessagesGetDialogsRequest{}, nil)
			if !errors.Is(err, failure) || calls != expectedCalls {
				t.Fatal("flood-wait bounds or cause changed", err, calls)
			}
		})
	}
}

func TestValidateConfigRejectsImplicitOrMixedEnvironments(t *testing.T) {
	t.Parallel()

	valid := Config{Environment: TestEnvironment, APIID: 12345, APIHash: []byte("0123456789abcdef0123456789abcdef"), TestDC: 2}
	if err := ValidateConfig(valid); err != nil {
		t.Fatalf("ValidateConfig(valid) error = %v", err)
	}
	if err := ValidateConfig(Config{Environment: ProductionEnvironment, APIID: valid.APIID, APIHash: valid.APIHash}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Config{
		{APIID: valid.APIID, APIHash: valid.APIHash, TestDC: 2},
		{Environment: ProductionEnvironment, APIID: valid.APIID, APIHash: valid.APIHash, TestDC: 2},
		{Environment: TestEnvironment, APIID: 0, APIHash: valid.APIHash, TestDC: 2},
		{Environment: TestEnvironment, APIID: valid.APIID, APIHash: valid.APIHash, TestDC: 0},
		{Environment: TestEnvironment, APIID: valid.APIID, APIHash: valid.APIHash, TestDC: 4},
		{Environment: TestEnvironment, APIID: valid.APIID, APIHash: []byte("not-a-secret-hash"), TestDC: 2},
	} {
		if err := ValidateConfig(invalid); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("ValidateConfig(%#v) error = %v", invalid, err)
		}
	}
}

func TestConcurrencyLimiterBoundsOutstandingRequests(t *testing.T) {
	t.Parallel()

	limiter := newConcurrencyLimiter(2)
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	invoker := gotdtelegram.InvokeFunc(func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if observed >= current || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		active.Add(-1)
		return ctx.Err()
	})
	wrapped := limiter.Handle(invoker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, 3)
	for index := 0; index < 3; index++ {
		go func() {
			_ = wrapped.Invoke(ctx, nil, nil)
			done <- struct{}{}
		}()
	}
	<-started
	<-started
	select {
	case <-started:
		t.Fatal("third request started before a concurrency slot was released")
	case <-time.After(100 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("third request did not start after a slot was released")
	}
	cancel()
	close(release)
	for index := 0; index < 3; index++ {
		<-done
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
}

func TestRuntimeErrorsAreCollapsedToStableSentinel(t *testing.T) {
	t.Parallel()

	const raw = "raw payload and secret"
	err := sanitizeAccountError(errors.New(raw))
	if !errors.Is(err, ErrTelegramUnavailable) || err.Error() == raw {
		t.Fatalf("sanitizeAccountError() = %v", err)
	}
}

func TestUnauthorizedRuntimeErrorsRequireReauthentication(t *testing.T) {
	t.Parallel()

	for name, sanitize := range map[string]func(error) error{
		"callback": sanitizeRuntimeError,
		"account":  sanitizeAccountError,
	} {
		t.Run(name, func(t *testing.T) {
			err := sanitize(errors.Join(context.Canceled, tgerr.New(401, "SESSION_EXPIRED")))
			if !errors.Is(err, ErrReauthenticationRequired) || errors.Is(err, ErrTelegramUnavailable) {
				t.Fatalf("sanitize(401) = %v", err)
			}
		})
	}
}

func TestAuthenticationRejectionRemainsDistinctFromAvailability(t *testing.T) {
	t.Parallel()

	err := sanitizeAuthenticationError(ErrAuthenticationRejected)
	if !errors.Is(err, ErrAuthenticationRejected) || errors.Is(err, ErrTelegramUnavailable) {
		t.Fatalf("sanitizeAuthenticationError() = %v", err)
	}
}

func TestInteractiveAuthenticatorClearsPromptBuffers(t *testing.T) {
	t.Parallel()

	phoneBytes := []byte("+9996621234")
	codeBytes := []byte("22222")
	prompt := &authPromptStub{phone: phoneBytes, code: codeBytes}
	authenticator := interactiveAuthenticator{prompt: prompt}
	phone, err := authenticator.Phone(context.Background())
	if err != nil || phone != "+9996621234" {
		t.Fatalf("Phone() = %q, %v", phone, err)
	}
	for _, value := range phoneBytes {
		if value != 0 {
			t.Fatal("Phone() did not clear the prompt buffer")
		}
	}
	code, err := authenticator.Code(context.Background(), nil)
	if err != nil || code != "22222" {
		t.Fatalf("Code() = %q, %v", code, err)
	}
	for _, value := range codeBytes {
		if value != 0 {
			t.Fatal("Code() did not clear the prompt buffer")
		}
	}
}

func TestInteractiveAuthenticatorClearsBuffersReturnedWithErrors(t *testing.T) {
	t.Parallel()

	promptError := errors.New("prompt failed")
	phoneBytes := []byte("sensitive phone")
	codeBytes := []byte("sensitive code")
	passwordBytes := []byte("sensitive password")
	prompt := &authPromptStub{
		phone:       phoneBytes,
		phoneErr:    promptError,
		code:        codeBytes,
		codeErr:     promptError,
		password:    passwordBytes,
		passwordErr: promptError,
	}
	authenticator := interactiveAuthenticator{prompt: prompt}
	if _, err := authenticator.Phone(context.Background()); !errors.Is(err, promptError) {
		t.Fatalf("Phone() error = %v", err)
	}
	if _, err := authenticator.Code(context.Background(), nil); !errors.Is(err, promptError) {
		t.Fatalf("Code() error = %v", err)
	}
	if _, err := authenticator.PasswordHash(context.Background(), nil); !errors.Is(err, promptError) {
		t.Fatalf("PasswordHash() error = %v", err)
	}
	for name, value := range map[string][]byte{
		"phone": phoneBytes, "code": codeBytes, "password": passwordBytes,
	} {
		for _, character := range value {
			if character != 0 {
				t.Fatalf("%s prompt buffer was not cleared", name)
			}
		}
	}
}

type authPromptStub struct {
	phone         []byte
	phoneErr      error
	code          []byte
	codeErr       error
	password      []byte
	passwordErr   error
	passwordCalls int
}

func (p *authPromptStub) Phone(context.Context) ([]byte, error) { return p.phone, p.phoneErr }
func (p *authPromptStub) Code(context.Context) ([]byte, error)  { return p.code, p.codeErr }
func (p *authPromptStub) Password(context.Context) ([]byte, error) {
	p.passwordCalls++
	return p.password, p.passwordErr
}
func (p *authPromptStub) ShowQRCode(context.Context, string, time.Time) error { return nil }

func TestAuthenticationDiagnosticsSurviveOuterSanitization(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input, want error
	}{
		{"password_missing", ErrPasswordRequired, ErrPasswordRequired},
		{"flood_wait", tgerr.New(420, "FLOOD_WAIT_300"), ErrAuthenticationRateLimited},
		{"phone_flood", tgerr.New(400, "PHONE_NUMBER_FLOOD"), ErrAuthenticationRateLimited},
		{"unknown", errors.New("sensitive remote details"), ErrTelegramUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeAccountError(sanitizeAuthenticationError(tc.input))
			if !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEmptyRequestedPasswordDoesNotComputeSRP(t *testing.T) {
	authenticator := interactiveAuthenticator{prompt: &authPromptStub{}}
	answer, err := authenticator.PasswordHash(context.Background(), nil)
	if answer != nil || !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("answer=%v error=%v", answer, err)
	}
}
