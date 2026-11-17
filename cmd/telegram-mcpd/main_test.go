package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/model"
	telegramruntime "github.com/lstpsche/telegram-mcp/internal/telegram"
)

func TestDaemonHelpHasNoSideEffects(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	factoryCalled := false
	code := runContext(
		context.Background(),
		[]string{"--help"},
		&stdout,
		&stderr,
		func() (daemonApplication, error) {
			factoryCalled = true
			return nil, nil
		},
	)
	if code != 0 || factoryCalled || stderr.Len() != 0 {
		t.Fatalf("code = %d, factoryCalled = %t, stderr = %q", code, factoryCalled, stderr.String())
	}
	if !strings.Contains(stdout.String(), "configure and authenticate through telegram-mcp") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDaemonRunsAndStopsCleanly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(
		ctx,
		nil,
		&stdout,
		&stderr,
		func() (daemonApplication, error) { return cleanDaemon{}, nil },
	)
	if code != 0 || stdout.Len() != 0 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "starting owner-only") || !strings.Contains(stderr.String(), "stopped cleanly") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDaemonFailureDoesNotExposeRawError(t *testing.T) {
	t.Parallel()

	const rawDetail = "raw Telegram payload secret"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(
		context.Background(),
		nil,
		&stdout,
		&stderr,
		func() (daemonApplication, error) { return failingDaemon{err: errors.New(rawDetail)}, nil },
	)
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("code = %d, stdout = %q", code, stdout.String())
	}
	if strings.Contains(stderr.String(), rawDetail) || !strings.Contains(stderr.String(), "failed safely") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDaemonReportsReauthenticationRequirement(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(
		context.Background(),
		nil,
		&stdout,
		&stderr,
		func() (daemonApplication, error) {
			return failingDaemon{err: telegramruntime.ErrReauthenticationRequired}, nil
		},
	)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "reauthenticate") {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestDaemonReportsSynchronizationFailuresWithoutPrivateCauses(t *testing.T) {
	t.Parallel()

	const privateDetail = "private Telegram response and credential bytes"
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "gap",
			err:  telegramruntime.ErrUpdateGap,
			want: "Telegram synchronization requires gap recovery; the last accepted checkpoint was retained",
		},
		{
			name: "recovery deadline",
			err:  errors.Join(telegramruntime.ErrUpdateRecoveryLimit, context.DeadlineExceeded),
			want: "Telegram synchronization recovery exceeded its time budget; accepted progress was retained",
		},
		{
			name: "stalled recovery",
			err:  telegramruntime.ErrUpdateRecoveryStalled,
			want: "Telegram synchronization recovery made no progress; the last accepted checkpoint was retained",
		},
		{
			name: "response size",
			err:  telegramruntime.ErrUpdateResponseLimit,
			want: "Telegram synchronization response exceeded its size limit; the last accepted checkpoint was retained",
		},
		{
			name: "invalid state",
			err:  telegramruntime.ErrUpdateStateInvalid,
			want: "Telegram synchronization rejected invalid state; the last accepted checkpoint was retained",
		},
		{
			name: "uncategorized synchronization cause",
			err:  errors.New(privateDetail),
			want: "Telegram synchronization failed; the last accepted checkpoint was retained",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cause := fmt.Errorf("%s: %w", privateDetail, test.err)
			err := model.TextError(model.ErrorFreshnessDegraded, cause)
			var stdout, stderr bytes.Buffer
			code := runContext(context.Background(), nil, &stdout, &stderr,
				func() (daemonApplication, error) { return failingDaemon{err: err}, nil })
			want := "telegram-mcpd: starting owner-only Telegram runtime\ntelegram-mcpd: " + test.want + "\n"
			if code != 1 || stdout.Len() != 0 || stderr.String() != want {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestDaemonDoesNotAssumeTestEnvironment(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := runContext(context.Background(), nil, &stdout, &stderr,
		func() (daemonApplication, error) { return nil, app.ErrConfigurationRequired })
	if code != 1 || stdout.Len() != 0 || stderr.String() != "telegram-mcpd: Telegram account configuration is required; run telegram-mcp configure\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

type cleanDaemon struct{}

func (cleanDaemon) RunDaemon(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

type failingDaemon struct {
	err error
}

func (f failingDaemon) RunDaemon(context.Context) error {
	return f.err
}

func TestKeychainProbeBypassesDaemon(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runContext(context.Background(), []string{"--keychain-probe", "read", "default.session"}, &stdout, &stderr,
		func() (daemonApplication, error) { t.Fatal("probe constructed daemon"); return nil, nil })
	if code != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "invalid_input") || strings.Contains(stdout.String(), "default.session") {
		t.Fatalf("code=%d output=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
