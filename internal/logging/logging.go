// Package logging is the only supported structured logging boundary for
// TgContext runtime packages. Its API accepts fixed events and a closed set of
// metadata fields; it intentionally has no free-form message or error field.
package logging

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

type Event string

const (
	EventProcessStarting   Event = "process.starting"
	EventProcessStopped    Event = "process.stopped"
	EventOperationFailed   Event = "operation.failed"
	EventReadinessChanged  Event = "readiness.changed"
	EventMigrationApplied  Event = "migration.applied"
	EventKeychainOperation Event = "keychain.operation"
)

func (event Event) valid() bool {
	switch event {
	case EventProcessStarting, EventProcessStopped, EventOperationFailed,
		EventReadinessChanged, EventMigrationApplied, EventKeychainOperation:
		return true
	default:
		return false
	}
}

type Component string

const (
	ComponentDaemon Component = "daemon"
	ComponentRelay  Component = "relay"
	ComponentCTL    Component = "control"
)

func (component Component) valid() bool {
	switch component {
	case ComponentDaemon, ComponentRelay, ComponentCTL:
		return true
	default:
		return false
	}
}

type Operation string

const (
	OperationMigrate        Operation = "migrate"
	OperationKeychainStore  Operation = "keychain_store"
	OperationKeychainRead   Operation = "keychain_read"
	OperationKeychainDelete Operation = "keychain_delete"
)

func (operation Operation) valid() bool {
	switch operation {
	case OperationMigrate, OperationKeychainStore, OperationKeychainRead, OperationKeychainDelete:
		return true
	default:
		return false
	}
}

type Field struct {
	attribute slog.Attr
}

func ComponentField(component Component) Field {
	value := "invalid"
	if component.valid() {
		value = string(component)
	}
	return Field{attribute: slog.String("component", value)}
}

func OperationField(operation Operation) Field {
	value := "invalid"
	if operation.valid() {
		value = string(operation)
	}
	return Field{attribute: slog.String("operation", value)}
}

func RequestIDField(requestID string) Field {
	if !model.IsValidRequestID(requestID) {
		requestID = "invalid"
	}
	return Field{attribute: slog.String("request_id", requestID)}
}

func ErrorCategoryField(category model.ErrorCategory) Field {
	value := "invalid"
	if category.IsValid() {
		value = string(category)
	}
	return Field{attribute: slog.String("error_category", value)}
}

func FreshnessField(state model.FreshnessState) Field {
	value := "invalid"
	if state.IsValid() {
		value = string(state)
	}
	return Field{attribute: slog.String("freshness", value)}
}

func VersionField(version string) Field {
	return Field{attribute: slog.String("version", safeToken(version))}
}

func CountField(count int) Field {
	if count < 0 {
		count = 0
	}
	return Field{attribute: slog.Int("count", count)}
}

func DurationField(duration time.Duration) Field {
	if duration < 0 {
		duration = 0
	}
	return Field{attribute: slog.Int64("duration_ms", duration.Milliseconds())}
}

func PartialField(partial bool) Field {
	return Field{attribute: slog.Bool("partial", partial)}
}

// Logger deliberately does not expose its underlying slog.Logger.
type Logger struct {
	logger *slog.Logger
}

func New(writer io.Writer, minimumLevel slog.Leveler) *Logger {
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: minimumLevel})
	return &Logger{logger: slog.New(handler)}
}

func (l *Logger) Info(ctx context.Context, event Event, fields ...Field) {
	l.log(ctx, slog.LevelInfo, event, fields)
}

func (l *Logger) Warn(ctx context.Context, event Event, fields ...Field) {
	l.log(ctx, slog.LevelWarn, event, fields)
}

func (l *Logger) Error(ctx context.Context, event Event, fields ...Field) {
	l.log(ctx, slog.LevelError, event, fields)
}

func (l *Logger) log(ctx context.Context, level slog.Level, event Event, fields []Field) {
	if !event.valid() {
		event = EventOperationFailed
		fields = nil
	}
	attributes := make([]any, 0, len(fields))
	for _, field := range fields {
		attributes = append(attributes, field.attribute)
	}
	l.logger.Log(ctx, level, string(event), attributes...)
}

var safeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func safeToken(value string) string {
	if !safeTokenPattern.MatchString(value) {
		return "invalid"
	}
	return value
}
