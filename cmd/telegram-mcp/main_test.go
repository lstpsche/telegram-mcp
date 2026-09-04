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

func TestRelayRejectsArgumentsWithoutLeakingThem(t *testing.T) {
	var stdout bufferOutput
	var stderr bytes.Buffer
	code := runContext(context.Background(), []string{"secret-shaped-input"}, io.NopCloser(strings.NewReader("")), &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || strings.Contains(stderr.String(), "secret-shaped-input") || !strings.Contains(stderr.String(), "invalid_input") {
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
