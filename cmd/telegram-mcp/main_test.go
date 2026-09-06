package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

type bufferOutput struct{ bytes.Buffer }

func (*bufferOutput) Close() error { return nil }

func TestHumanCommandFailureDoesNotLeakArguments(t *testing.T) {
	var stdout bufferOutput
	var stderr bytes.Buffer
	code := runContext(context.Background(), []string{"secret-shaped-input"}, io.NopCloser(strings.NewReader("")), &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || strings.Contains(stderr.String(), "secret-shaped-input") || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRelayVersionIsExplicitOperatorOutput(t *testing.T) {
	var stdout bufferOutput
	var stderr bytes.Buffer
	code := runContext(context.Background(), []string{"--version"}, io.NopCloser(strings.NewReader("")), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Telegram MCP telegram-mcp") || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestHumanSubcommandsUseUnifiedCommand(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		code   int
		output string
	}{
		{[]string{"--help"}, 0, "telegram-mcp scope setup"},
		{[]string{"-h"}, 0, "Without arguments"},
		{[]string{"install", "--version", "invalid"}, 2, "telegram-mcp: use install"},
		{[]string{"setup", "unexpected"}, 2, "telegram-mcp: setup accepts no arguments"},
	} {
		var stdout bufferOutput
		var stderr bytes.Buffer
		code := runContext(context.Background(), tc.args, io.NopCloser(strings.NewReader("")), &stdout, &stderr)
		if code != tc.code || !strings.Contains(stdout.String()+stderr.String(), tc.output) {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", tc.args, code, stdout.String(), stderr.String())
		}
		if tc.code != 0 && stdout.Len() != 0 {
			t.Fatal("failed command emitted success output")
		}
	}
}
