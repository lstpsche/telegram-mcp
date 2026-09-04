package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func TestLoggerEmitsOnlyFixedEventAndMetadataFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := New(&output, slog.LevelInfo)
	logger.Info(
		context.Background(),
		EventProcessStarting,
		ComponentField(ComponentDaemon),
		OperationField(OperationMigrate),
		RequestIDField("req_phase0_1"),
		FreshnessField(model.FreshnessLive),
		CountField(2),
		DurationField(1500*time.Millisecond),
		PartialField(false),
	)

	line := output.String()
	for _, expected := range []string{
		`"msg":"process.starting"`,
		`"component":"daemon"`,
		`"request_id":"req_phase0_1"`,
		`"duration_ms":1500`,
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("log line %s does not contain %s", line, expected)
		}
	}
}

func TestLoggerDoesNotEchoInvalidEventOrFieldValue(t *testing.T) {
	t.Parallel()

	const hostile = "third-party message body\nsecret"
	var output bytes.Buffer
	logger := New(&output, slog.LevelInfo)
	logger.Error(
		context.Background(),
		Event(hostile),
		RequestIDField(hostile),
		ErrorCategoryField(model.ErrorInternal),
	)

	line := output.String()
	if strings.Contains(line, hostile) || strings.Contains(line, "third-party") || strings.Contains(line, "secret") {
		t.Fatalf("hostile content reached log output: %s", line)
	}
	if !strings.Contains(line, `"msg":"operation.failed"`) {
		t.Fatalf("invalid event did not fail closed: %s", line)
	}
}

func TestLoggerRejectsHostileMetadataOnValidEvent(t *testing.T) {
	t.Parallel()

	const hostile = "third-party message body\nsecret"
	var output bytes.Buffer
	logger := New(&output, slog.LevelInfo)
	logger.Info(
		context.Background(),
		EventProcessStarting,
		ComponentField(Component(hostile)),
		OperationField(Operation(hostile)),
		RequestIDField(hostile),
		ErrorCategoryField(model.ErrorCategory(hostile)),
		FreshnessField(model.FreshnessState(hostile)),
	)

	line := output.String()
	if strings.Contains(line, "third-party") || strings.Contains(line, "secret") {
		t.Fatalf("hostile metadata reached log output: %s", line)
	}
	if got := strings.Count(line, `"invalid"`); got != 5 {
		t.Fatalf("invalid metadata count = %d, log = %s", got, line)
	}
}
