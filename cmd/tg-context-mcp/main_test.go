package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnavailableRelayKeepsStdoutFrameClean(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("relay diagnostic reached stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unavailable in Phase 0") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRelayVersionIsExplicitOperatorOutput(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "TgContext tg-context-mcp") || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}
