package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/daemon"
	metastore "github.com/lstpsche/telegram-mcp/internal/store"
	tgaccount "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestControlPlaneHelpListsOnlyHumanCommands(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	for _, command := range []string{"configure", "auth {phone|qr}", "restore --dry-run", "status", "logout"} {
		if !strings.Contains(stdout.String(), command) {
			t.Fatalf("help does not include %q: %q", command, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), "--production --attest-eligible") || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestConfigureKeepsAPIHashOffProcessStreams(t *testing.T) {
	t.Parallel()

	const secret = "0123456789abcdef0123456789abcdef"
	control := &fakeController{}
	prompt := &fakeTerminal{apiID: 12345, apiHash: []byte(secret)}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(
		context.Background(),
		[]string{"configure", "--test-dc", "2"},
		&stdout,
		&stderr,
		func() (controller, error) { return control, nil },
		func() (terminal, error) { return prompt, nil },
	)
	if code != 0 {
		t.Fatalf("runContext() code = %d, stderr = %q", code, stderr.String())
	}
	if control.configuredAPIID != 12345 || control.configuredDC != 2 || string(control.configuredHash) != secret {
		t.Fatalf("Configure() values = %d, %d, %q", control.configuredAPIID, control.configuredDC, control.configuredHash)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatal("API hash appeared in a process stream")
	}
	if !prompt.closed {
		t.Fatal("interactive terminal was not closed")
	}
}

func TestConfigureRejectsIneligibleRequestBeforeOpeningTerminal(t *testing.T) {
	t.Parallel()

	control := &fakeController{configureError: metastore.ErrAuthorizationExists}
	terminalOpened := false
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(
		context.Background(),
		[]string{"configure", "--test-dc", "2"},
		&stdout,
		&stderr,
		func() (controller, error) { return control, nil },
		func() (terminal, error) {
			terminalOpened = true
			return &fakeTerminal{}, nil
		},
	)
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("runContext() code = %d, stdout = %q", code, stdout.String())
	}
	if terminalOpened {
		t.Fatal("configure opened the terminal before eligibility was established")
	}
	if !strings.Contains(stderr.String(), "log out before changing") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAuthErrorsAreSanitized(t *testing.T) {
	t.Parallel()

	const sensitiveError = "raw upstream detail with 12345"
	control := &fakeController{authError: errors.New(sensitiveError)}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(
		context.Background(),
		[]string{"auth", "phone"},
		&stdout,
		&stderr,
		func() (controller, error) { return control, nil },
		func() (terminal, error) { return &fakeTerminal{}, nil },
	)
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("runContext() code = %d, stdout = %q", code, stdout.String())
	}
	if strings.Contains(stderr.String(), sensitiveError) || !strings.Contains(stderr.String(), "failed safely") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAuthenticationRejectsSecretBearingArgumentsWithoutEcho(t *testing.T) {
	t.Parallel()

	const accidentalSecret = "do-not-echo-this-code"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(
		context.Background(),
		[]string{"auth", "phone", accidentalSecret},
		&stdout,
		&stderr,
		func() (controller, error) { return &fakeController{}, nil },
		func() (terminal, error) { return nil, errors.New("must not open") },
	)
	if code != 2 || strings.Contains(stdout.String(), accidentalSecret) || strings.Contains(stderr.String(), accidentalSecret) {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestStatusSeparatesCapabilityFromAccountState(t *testing.T) {
	t.Parallel()

	control := &fakeController{status: app.Status{
		Daemon:           daemon.SocketLive,
		Configured:       true,
		Environment:      tgaccount.TestEnvironment,
		TestDC:           3,
		Authorized:       true,
		PhoneCheckPassed: true,
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(
		context.Background(),
		[]string{"status"},
		&stdout,
		&stderr,
		func() (controller, error) { return control, nil },
		func() (terminal, error) { return nil, errors.New("must not open") },
	)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runContext() code = %d, stderr = %q", code, stderr.String())
	}
	for _, expected := range []string{
		"daemon=live",
		"environment=test",
		"test_dc=3",
		"authorization_recorded=true",
		"phone_check=true",
		"qr_check=false",
		"production_login=supported",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status missing %q: %q", expected, stdout.String())
		}
	}
}

func TestParseConfigurationRequiresExplicitEnvironment(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		nil,
		{"--test-dc", "0"},
		{"--test-dc", "4"},
		{"--test-dc", "2", "extra"},
		{"--production"},
	} {
		if _, dc, ok := parseConfiguration(args); ok {
			t.Fatalf("parseConfiguration(%q) = %d, true", args, dc)
		}
	}
	if _, dc, ok := parseConfiguration([]string{"--test-dc", "2"}); !ok || dc != 2 {
		t.Fatalf("parseConfiguration(valid) = %d, %t", dc, ok)
	}
}

type fakeController struct {
	configuredAPIID       int
	configuredDC          int
	configuredEnvironment string
	configuredHash        []byte
	configureError        error
	authOutcome           app.AuthOutcome
	authError             error
	logoutError           error
	status                app.Status
	statusError           error
}

func (f *fakeController) Configure(ctx context.Context, environment string, testDC int, read app.ConfigurationReader) error {
	if f.configureError != nil {
		return f.configureError
	}
	apiID, apiHash, err := read(ctx)
	defer clear(apiHash)
	if err != nil {
		return err
	}
	f.configuredAPIID = apiID
	f.configuredDC = testDC
	f.configuredEnvironment = environment
	f.configuredHash = append([]byte(nil), apiHash...)
	return nil
}

func (f *fakeController) Authenticate(
	context.Context,
	tgaccount.AuthMethod,
	tgaccount.Prompt,
) (app.AuthOutcome, error) {
	return f.authOutcome, f.authError
}

func (f *fakeController) Logout(context.Context) error {
	return f.logoutError
}

func (f *fakeController) Status(context.Context) (app.Status, error) {
	return f.status, f.statusError
}

type fakeTerminal struct {
	apiID   int
	apiHash []byte
	closed  bool
}

func (f *fakeTerminal) ReadAPIID(context.Context) (int, error) {
	return f.apiID, nil
}

func (f *fakeTerminal) ReadAPIHash(context.Context) ([]byte, error) {
	return append([]byte(nil), f.apiHash...), nil
}

func (f *fakeTerminal) Phone(context.Context) ([]byte, error) {
	return []byte("9996621234"), nil
}

func (f *fakeTerminal) Code(context.Context) ([]byte, error) {
	return []byte("22222"), nil
}

func (f *fakeTerminal) Password(context.Context) ([]byte, error) {
	return []byte("password"), nil
}

func (f *fakeTerminal) ShowQRCode(context.Context, string, time.Time) error {
	return nil
}

func (f *fakeTerminal) Close() error {
	f.closed = true
	return nil
}

func TestKeychainProbeBypassesControlPlane(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runContext(context.Background(), []string{"--keychain-probe", "read", "default.session"}, &stdout, &stderr,
		func() (controller, error) { t.Fatal("probe constructed account controller"); return nil, nil },
		func() (terminal, error) { t.Fatal("probe opened terminal"); return nil, nil })
	if code != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "invalid_input") || strings.Contains(stdout.String(), "default.session") {
		t.Fatalf("code=%d output=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestProductionConfigurationRequiresAttestationBeforeSecrets(t *testing.T) {
	for _, args := range [][]string{
		{"configure", "--production"},
		{"configure", "--production", "--test-dc", "2", "--attest-eligible"},
		{"configure", "--test-dc", "2", "--test-dc", "3"},
		{"configure", "--production", "--attest-eligible", "unexpected"},
	} {
		control := &fakeController{}
		var stdout, stderr bytes.Buffer
		code := runContext(context.Background(), args, &stdout, &stderr, func() (controller, error) { return control, nil }, func() (terminal, error) { t.Fatal("invalid selection opened terminal"); return nil, nil })
		if code != 2 || control.configuredAPIID != 0 {
			t.Fatalf("invalid selection accepted: %v", args)
		}
	}
	control := &fakeController{}
	var stdout, stderr bytes.Buffer
	code := runContext(context.Background(), []string{"configure", "--production", "--attest-eligible"}, &stdout, &stderr, func() (controller, error) { return control, nil }, func() (terminal, error) {
		return &fakeTerminal{apiID: 12345, apiHash: []byte("0123456789abcdef0123456789abcdef")}, nil
	})
	if code != 0 || control.configuredEnvironment != tgaccount.ProductionEnvironment || control.configuredDC != 0 {
		t.Fatal("explicit production configuration failed", stderr.String())
	}
	if strings.Contains(stdout.String(), "0123456789abcdef") || strings.Contains(stderr.String(), "0123456789abcdef") {
		t.Fatal("credential leaked")
	}
}

func TestAuthenticationDiagnosticsAreDistinctAndContentFree(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{tgaccount.ErrPasswordRequired, "empty value cannot skip it"},
		{tgaccount.ErrAuthenticationRateLimited, "rate-limiting authentication"},
	} {
		var output bytes.Buffer
		writeControlError(&output, errors.Join(errors.New("sensitive remote details"), tc.err))
		if !strings.Contains(output.String(), tc.want) || strings.Contains(output.String(), "sensitive") || strings.Contains(output.String(), "operation is unavailable") {
			t.Fatal("authentication diagnostic lost or leaked", output.String())
		}
	}
}
