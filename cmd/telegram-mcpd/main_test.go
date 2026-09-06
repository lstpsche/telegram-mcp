package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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
