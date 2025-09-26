package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

type eventRecord struct {
	Level  string
	Msg    string
	Err    error
	Fields map[string]any
}

type recordingLogger struct {
	sink   *recordingSink
	fields map[string]any
}

type recordingSink struct {
	events []*eventRecord
}

type recordingEvent struct {
	record *eventRecord
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{
		sink:   &recordingSink{},
		fields: map[string]any{},
	}
}

func (l *recordingLogger) clone() *recordingLogger {
	cloned := &recordingLogger{
		sink:   l.sink,
		fields: make(map[string]any, len(l.fields)),
	}
	for k, v := range l.fields {
		cloned.fields[k] = v
	}
	return cloned
}

func (l *recordingLogger) newEvent(level string) logger.LogEvent {
	record := &eventRecord{
		Level:  level,
		Fields: make(map[string]any, len(l.fields)),
	}
	for k, v := range l.fields {
		record.Fields[k] = v
	}
	l.sink.events = append(l.sink.events, record)
	return &recordingEvent{record: record}
}

func (l *recordingLogger) Info() logger.LogEvent  { return l.newEvent(levelInfo) }
func (l *recordingLogger) Error() logger.LogEvent { return l.newEvent(levelError) }
func (l *recordingLogger) Debug() logger.LogEvent { return l.newEvent(levelDebug) }
func (l *recordingLogger) Warn() logger.LogEvent  { return l.newEvent(levelWarn) }
func (l *recordingLogger) Fatal() logger.LogEvent { return l.newEvent(levelFatal) }

func (l *recordingLogger) WithContext(_ any) logger.Logger { return l.clone() }

func (l *recordingLogger) WithFields(fields map[string]any) logger.Logger {
	cloned := l.clone()
	for k, v := range fields {
		cloned.fields[k] = v
	}
	return cloned
}

func (l *recordingLogger) events() []*eventRecord {
	return l.sink.events
}

func (e *recordingEvent) Msg(msg string) {
	e.record.Msg = msg
}

func (e *recordingEvent) Msgf(format string, args ...any) {
	e.record.Msg = fmt.Sprintf(format, args...)
}

func (e *recordingEvent) Err(err error) logger.LogEvent {
	e.record.Err = err
	return e
}

func (e *recordingEvent) Str(key, value string) logger.LogEvent {
	e.record.Fields[key] = value
	return e
}

func (e *recordingEvent) Int(key string, value int) logger.LogEvent {
	e.record.Fields[key] = value
	return e
}

func (e *recordingEvent) Int64(key string, value int64) logger.LogEvent {
	e.record.Fields[key] = value
	return e
}

func (e *recordingEvent) Uint64(key string, value uint64) logger.LogEvent {
	e.record.Fields[key] = value
	return e
}

func (e *recordingEvent) Dur(key string, d time.Duration) logger.LogEvent {
	e.record.Fields[key] = d
	return e
}

func (e *recordingEvent) Interface(key string, value any) logger.LogEvent {
	e.record.Fields[key] = value
	return e
}

func (e *recordingEvent) Bytes(key string, value []byte) logger.LogEvent {
	copied := append([]byte(nil), value...)
	e.record.Fields[key] = copied
	return e
}

func TestTruncateStringNoTruncation(t *testing.T) {
	original := "short"
	if TruncateString(original, len(original)+1) != original {
		t.Fatalf("expected string to remain unchanged when shorter than max")
	}
	if TruncateString(original, 0) != original {
		t.Fatalf("expected string to remain unchanged when max <= 0")
	}
}

func TestTruncateStringShortMax(t *testing.T) {
	value := "abcdef"
	if got := TruncateString(value, 3); got != "abc" {
		t.Fatalf("expected first max characters when max <= 3, got %q", got)
	}
}

func TestTruncateStringAddsEllipsis(t *testing.T) {
	value := "abcdefghij"
	if got := TruncateString(value, 6); got != "abc..." {
		t.Fatalf("unexpected truncated result: %q", got)
	}
}

func TestSanitizeArgsHandlesVariousTypes(t *testing.T) {
	args := []any{"verylong", []byte{0x01, 0x02}, 12345}
	sanitized := SanitizeArgs(args, 5)
	if sanitized[0] != "ve..." {
		t.Fatalf("expected truncated string, got %v", sanitized[0])
	}
	if sanitized[1] != "<bytes len=2>" {
		t.Fatalf("expected byte placeholder, got %v", sanitized[1])
	}
	if sanitized[2] != "12345" {
		t.Fatalf("expected formatted scalar, got %v", sanitized[2])
	}
}

func TestSanitizeArgsReturnsNilForEmptySlice(t *testing.T) {
	if SanitizeArgs(nil, 10) != nil {
		t.Fatalf("expected nil for empty input")
	}
	if SanitizeArgs([]any{}, 10) != nil {
		t.Fatalf("expected nil for empty slice")
	}
}

func TestTrackDBOperationRecordsSuccess(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLogger()
	settings := Settings{
		slowQueryThreshold: time.Second,
		maxQueryLength:     50,
		logQueryParameters: false,
	}

	start := time.Now().Add(-25 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, "SELECT 1", []any{"param"}, start, nil)

	if logger.GetDBCounter(ctx) != 1 {
		t.Fatalf("expected db counter to increment")
	}
	elapsed := logger.GetDBElapsed(ctx)
	if elapsed <= 0 {
		t.Fatalf("expected elapsed time to be recorded")
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected a single log event, got %d", len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level, got %s", event.Level)
	}
	if event.Msg != "Database operation executed" {
		t.Fatalf("unexpected log message: %q", event.Msg)
	}
	if event.Fields["query"] != "SELECT 1" {
		t.Fatalf("expected query field to be stored")
	}
	if _, exists := event.Fields["args"]; exists {
		t.Fatalf("did not expect args when LogQueryParameters is false")
	}
}

func TestTrackDBOperationTruncatesQueryAndLogsArgs(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLogger()
	settings := Settings{
		slowQueryThreshold: time.Second,
		maxQueryLength:     5,
		logQueryParameters: true,
	}

	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "oracle", Settings: settings}, "SELECT something", []any{"verylongparameter", []byte{0x1, 0x2}}, start, nil)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected a single event, got %d", len(events))
	}
	event := events[0]
	if event.Fields["query"].(string) != "SE..." {
		t.Fatalf("expected truncated query, got %v", event.Fields["query"])
	}
	argsField, ok := event.Fields["args"].([]any)
	if !ok {
		t.Fatalf("expected args field to be []any, got %T", event.Fields["args"])
	}
	if argsField[0] != "ve..." {
		t.Fatalf("expected sanitized string argument, got %v", argsField[0])
	}
	if argsField[1] != "<bytes len=2>" {
		t.Fatalf("expected sanitized byte argument, got %v", argsField[1])
	}
}

func TestTrackDBOperationLogsSlowQuery(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLogger()
	settings := Settings{
		slowQueryThreshold: 5 * time.Millisecond,
		maxQueryLength:     100,
	}

	start := time.Now().Add(-20 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, "SELECT 1", nil, start, nil)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected a single event, got %d", len(events))
	}
	event := events[0]
	if event.Level != levelWarn {
		t.Fatalf("expected warn level for slow query, got %s", event.Level)
	}
	if event.Msg == "" || event.Msg == "Database operation executed" {
		t.Fatalf("expected slow query warning message, got %q", event.Msg)
	}
}

func TestTrackDBOperationHandlesSqlErrNoRows(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLogger()
	settings := Settings{slowQueryThreshold: time.Second}

	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, "SELECT 1", nil, start, sql.ErrNoRows)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected a single event, got %d", len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level for sql.ErrNoRows, got %s", event.Level)
	}
	if event.Msg != "Database operation returned no rows" {
		t.Fatalf("unexpected message: %q", event.Msg)
	}
}

func TestTrackDBOperationLogsErrors(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLogger()
	settings := Settings{slowQueryThreshold: time.Second}

	failure := errors.New("boom")
	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, "SELECT 1", nil, start, failure)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf("expected a single event, got %d", len(events))
	}
	event := events[0]
	if event.Level != levelError {
		t.Fatalf("expected error level, got %s", event.Level)
	}
	if event.Err != failure {
		t.Fatalf("expected error to be recorded")
	}
	if event.Msg != "Database operation error" {
		t.Fatalf("unexpected message: %q", event.Msg)
	}
}

func TestTrackDBOperationNoLoggerOrContext(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic when logger is nil: %v", r)
		}
	}()
	TrackDBOperation(context.Background(), nil, "SELECT", nil, time.Now(), nil)
	TrackDBOperation(context.Background(), &Context{}, "SELECT", nil, time.Now(), nil)
}
