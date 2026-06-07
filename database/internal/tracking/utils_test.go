package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
)

const (
	singleEventExpected = "expected a single log event, got %d"
)

type eventRecord struct {
	Level  string
	Msg    string
	Err    error
	Fields map[string]any
}

// recordingLogger is a recording SPY: it records every adapter method call —
// including calls made on disabled events (e.g. Msg on a dropped WARN) — so tests
// can assert exactly what production invoked. Enabled() faithfully reports the
// per-level state (via disabledLevels), but the spy intentionally does NOT emulate
// zerolog's drop-the-line behavior: a disabled event still records its Msg and any
// fields set on it, letting tests verify the severity-escalation path (Msg is called
// on a disabled WARN/ERROR) while also asserting the field-build short-circuit.
type recordingLogger struct {
	sink   *recordingSink
	fields map[string]any
	// disabledLevels holds level names for which the produced event reports
	// Enabled() == false and records no fields — emulating zerolog dropping
	// events below the configured threshold (e.g. debug at LOG_LEVEL=info).
	disabledLevels map[string]bool
}

type recordingSink struct {
	events []*eventRecord
}

type recordingEvent struct {
	record  *eventRecord
	enabled bool
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{
		sink:           &recordingSink{},
		fields:         map[string]any{},
		disabledLevels: map[string]bool{},
	}
}

// newRecordingLoggerWithDisabled returns a recordingLogger whose events at the given
// levels report Enabled() == false (and therefore must not have fields built on them).
func newRecordingLoggerWithDisabled(levels ...string) *recordingLogger {
	l := newRecordingLogger()
	for _, lvl := range levels {
		l.disabledLevels[lvl] = true
	}
	return l
}

func (l *recordingLogger) clone() *recordingLogger {
	cloned := &recordingLogger{
		sink:           l.sink,
		fields:         make(map[string]any, len(l.fields)),
		disabledLevels: make(map[string]bool, len(l.disabledLevels)),
	}
	for k, v := range l.fields {
		cloned.fields[k] = v
	}
	// Deep-copy disabledLevels too (symmetry with fields); avoids a clone sharing
	// the parent's level map and decoupling the spy's enablement state.
	for k, v := range l.disabledLevels {
		cloned.disabledLevels[k] = v
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
	return &recordingEvent{record: record, enabled: !l.disabledLevels[level]}
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

func (e *recordingEvent) Bool(key string, value bool) logger.LogEvent {
	e.record.Fields[key] = value
	return e
}

func (e *recordingEvent) Enabled() bool { return e.enabled }

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
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, []any{"param"}, start, 0, nil)

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
	if event.Msg != msgDBOperationExecuted {
		t.Fatalf("unexpected log message: %q", event.Msg)
	}
	if event.Fields["query"] != selectOne {
		t.Fatalf("expected query field to be stored")
	}
	if _, exists := event.Fields["args"]; exists {
		t.Fatalf("did not expect args when LogQueryParameters is false")
	}
}

// TestTrackDBOperationSkipsFieldBuildWhenDebugDisabled verifies the success fast path:
// when the debug level is disabled (e.g. LOG_LEVEL=info on a healthy query), no log
// fields are built, yet the DB counter/elapsed metrics still fire — proving the level
// short-circuit never gates observability. Msg is still called on the disabled event so
// the severity-parity path runs (a no-op at debug level, but exercised identically to
// the WARN/ERROR branches, where escalation must be preserved).
func TestTrackDBOperationSkipsFieldBuildWhenDebugDisabled(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLoggerWithDisabled(levelDebug)
	settings := Settings{
		slowQueryThreshold: time.Second,
		maxQueryLength:     50,
		logQueryParameters: true,
	}

	start := time.Now().Add(-25 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, []any{"param"}, start, 0, nil)

	// Metrics/counters must still fire even though the debug log is disabled.
	if logger.GetDBCounter(ctx) != 1 {
		t.Fatalf("expected db counter to increment even when debug log is disabled")
	}
	if logger.GetDBElapsed(ctx) <= 0 {
		t.Fatalf("expected elapsed time to be recorded even when debug log is disabled")
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level event, got %s", event.Level)
	}
	// Short-circuit must prevent any field construction.
	if len(event.Fields) != 0 {
		t.Fatalf("expected no fields built when level disabled, got %v", event.Fields)
	}
	// Msg must still be called on the disabled event so the severity-parity path runs.
	if event.Msg != msgDBOperationExecuted {
		t.Fatalf("expected success message even when level disabled (Msg still called), got %q", event.Msg)
	}
}

// TestTrackDBOperationDisabledWarnStillEscalates verifies the disabled-WARN slow-query
// path: when the warn level is disabled, no log fields are built (the allocation win),
// but event.Msg is still called with the slow-query message — proving severity escalation
// is preserved (the adapter's Msg -> trackSeverity hook fires regardless of Enabled()).
func TestTrackDBOperationDisabledWarnStillEscalates(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLoggerWithDisabled(levelWarn)
	settings := Settings{
		slowQueryThreshold: 5 * time.Millisecond,
		maxQueryLength:     100,
		logQueryParameters: true,
	}

	start := time.Now().Add(-20 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, []any{"param"}, start, 0, nil)

	if logger.GetDBCounter(ctx) != 1 {
		t.Fatalf("expected db counter to increment even when warn log is disabled")
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelWarn {
		t.Fatalf("expected warn level event, got %s", event.Level)
	}
	// Field construction must be short-circuited.
	if len(event.Fields) != 0 {
		t.Fatalf("expected no fields built when warn level disabled, got %v", event.Fields)
	}
	// Msg must still be called so the WARN escalates request severity.
	elapsed := logger.GetDBElapsed(ctx)
	if elapsed <= 0 {
		t.Fatalf("expected elapsed time to be recorded")
	}
	expectedMsg := fmt.Sprintf("Slow database operation detected (%s)", time.Duration(elapsed))
	if event.Msg != expectedMsg {
		t.Fatalf("expected slow-query message on disabled WARN, got %q", event.Msg)
	}
}

// TestTrackDBOperationDisabledErrorStillEscalates verifies the disabled-ERROR path:
// when the error level is disabled, no fields are built but event.Msg is still called
// with the error message and the error is attached — proving severity escalation is
// preserved on a dropped ERROR line.
func TestTrackDBOperationDisabledErrorStillEscalates(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLoggerWithDisabled(levelError)
	settings := Settings{slowQueryThreshold: time.Second}

	failure := errors.New("boom")
	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, nil, start, 0, failure)

	if logger.GetDBCounter(ctx) != 1 {
		t.Fatalf("expected db counter to increment even when error log is disabled")
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelError {
		t.Fatalf("expected error level event, got %s", event.Level)
	}
	// The error is attached via .Err(err) before the Enabled() short-circuit; the
	// fields map itself must remain empty (no vendor/duration/query built).
	if len(event.Fields) != 0 {
		t.Fatalf("expected no fields built when error level disabled, got %v", event.Fields)
	}
	if event.Err != failure {
		t.Fatalf("expected error to be attached on disabled ERROR, got %v", event.Err)
	}
	if event.Msg != msgDBOperationError {
		t.Fatalf("expected error message on disabled ERROR, got %q", event.Msg)
	}
}

// TestTrackDBOperationDisabledDebugErrNoRows verifies the disabled-DEBUG sql.ErrNoRows
// path short-circuits field construction yet still records its benign message and
// increments the counter (trackSeverity is a no-op below WarnLevel).
func TestTrackDBOperationDisabledDebugErrNoRows(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLoggerWithDisabled(levelDebug)
	settings := Settings{slowQueryThreshold: time.Second}

	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, nil, start, 0, sql.ErrNoRows)

	if logger.GetDBCounter(ctx) != 1 {
		t.Fatalf("expected db counter to increment even when debug log is disabled")
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level for sql.ErrNoRows, got %s", event.Level)
	}
	if len(event.Fields) != 0 {
		t.Fatalf("expected no fields built when debug level disabled, got %v", event.Fields)
	}
	if event.Msg != msgDBOperationNoRows {
		t.Fatalf("unexpected message on disabled ErrNoRows: %q", event.Msg)
	}
}

// TestTrackDBOperationDisabledDebugErrTxDone verifies the disabled-DEBUG sql.ErrTxDone
// path short-circuits field construction yet still records its benign message and
// increments the counter.
func TestTrackDBOperationDisabledDebugErrTxDone(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLoggerWithDisabled(levelDebug)
	settings := Settings{slowQueryThreshold: time.Second}

	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, "ROLLBACK", nil, start, 0, sql.ErrTxDone)

	if logger.GetDBCounter(ctx) != 1 {
		t.Fatalf("expected db counter to increment even when debug log is disabled")
	}

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level for sql.ErrTxDone, got %s", event.Level)
	}
	if len(event.Fields) != 0 {
		t.Fatalf("expected no fields built when debug level disabled, got %v", event.Fields)
	}
	if event.Msg != msgDBTxFinalized {
		t.Fatalf("unexpected message on disabled ErrTxDone: %q", event.Msg)
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
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "oracle", Settings: settings}, "SELECT something", []any{"verylongparameter", []byte{0x1, 0x2}}, start, 0, nil)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
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
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, nil, start, 0, nil)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelWarn {
		t.Fatalf("expected warn level for slow query, got %s", event.Level)
	}
	if !strings.HasPrefix(event.Msg, "Slow database operation detected (") {
		t.Fatalf("expected slow query warning message, got %q", event.Msg)
	}
}

func TestTrackDBOperationHandlesSqlErrNoRows(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLogger()
	settings := Settings{slowQueryThreshold: time.Second}

	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, nil, start, 0, sql.ErrNoRows)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level for sql.ErrNoRows, got %s", event.Level)
	}
	if event.Msg != msgDBOperationNoRows {
		t.Fatalf("unexpected message: %q", event.Msg)
	}
}

func TestTrackDBOperationHandlesSqlErrTxDone(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLogger()
	settings := Settings{slowQueryThreshold: time.Second}

	start := time.Now().Add(-10 * time.Millisecond)
	// A deferred Rollback after a successful Commit returns sql.ErrTxDone; it must
	// be treated as benign (debug), not logged at error level.
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, "ROLLBACK", nil, start, 0, sql.ErrTxDone)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelDebug {
		t.Fatalf("expected debug level for sql.ErrTxDone, got %s", event.Level)
	}
	if event.Msg != msgDBTxFinalized {
		t.Fatalf("unexpected message: %q", event.Msg)
	}
}

func TestTrackDBOperationLogsErrors(t *testing.T) {
	ctx := logger.WithDBCounter(context.Background())
	recLogger := newRecordingLogger()
	settings := Settings{slowQueryThreshold: time.Second}

	failure := errors.New("boom")
	start := time.Now().Add(-10 * time.Millisecond)
	TrackDBOperation(ctx, &Context{Logger: recLogger, Vendor: "postgresql", Settings: settings}, selectOne, nil, start, 0, failure)

	events := recLogger.events()
	if len(events) != 1 {
		t.Fatalf(singleEventExpected, len(events))
	}
	event := events[0]
	if event.Level != levelError {
		t.Fatalf("expected error level, got %s", event.Level)
	}
	if event.Err != failure {
		t.Fatalf("expected error to be recorded")
	}
	if event.Msg != msgDBOperationError {
		t.Fatalf("unexpected message: %q", event.Msg)
	}
}

func TestTrackDBOperationNoLoggerOrContext(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic when logger is nil: %v", r)
		}
	}()
	TrackDBOperation(context.Background(), nil, "SELECT", nil, time.Now(), 0, nil)
	TrackDBOperation(context.Background(), &Context{}, "SELECT", nil, time.Now(), 0, nil)
}

// TestExtractRowsAffected tests the extractRowsAffected helper function.
func TestExtractRowsAffected(t *testing.T) {
	tests := []struct {
		name     string
		result   sql.Result
		err      error
		expected int64
	}{
		{
			name:     "nil_result",
			result:   nil,
			err:      nil,
			expected: 0,
		},
		{
			name:     "error_present",
			result:   &mockResult{rows: 5},
			err:      errors.New("database error"),
			expected: 0,
		},
		{
			name:     "successful_operation",
			result:   &mockResult{rows: 10},
			err:      nil,
			expected: 10,
		},
		{
			name:     "zero_rows_affected",
			result:   &mockResult{rows: 0},
			err:      nil,
			expected: 0,
		},
		{
			name:     "rows_affected_error",
			result:   &mockResult{rows: 5, rowsErr: errors.New("rows affected failed")},
			err:      nil,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractRowsAffected(tt.result, tt.err)
			assert.Equal(t, tt.expected, result, "extractRowsAffected should return expected value")
		})
	}
}

// mockResult is a mock implementation of sql.Result for testing.
type mockResult struct {
	rows    int64
	lastID  int64
	rowsErr error
}

func (m *mockResult) LastInsertId() (int64, error) {
	return m.lastID, nil
}

func (m *mockResult) RowsAffected() (int64, error) {
	if m.rowsErr != nil {
		return 0, m.rowsErr
	}
	return m.rows, nil
}

// TestBuildPostgreSQLNamespace tests the PostgreSQL namespace builder.
func TestBuildPostgreSQLNamespace(t *testing.T) {
	tests := []struct {
		name     string
		database string
		schema   string
		expected string
	}{
		{
			name:     "withDatabaseAndSchema",
			database: "mydb",
			schema:   "public",
			expected: "mydb.public",
		},
		{
			name:     "withCustomSchema",
			database: "mydb",
			schema:   "analytics",
			expected: "mydb.analytics",
		},
		{
			name:     "emptyDatabase",
			database: "",
			schema:   "public",
			expected: "",
		},
		{
			name:     "emptySchema",
			database: "mydb",
			schema:   "",
			expected: "",
		},
		{
			name:     "bothEmpty",
			database: "",
			schema:   "",
			expected: "",
		},
		{
			name:     "complexNames",
			database: "my_app_db",
			schema:   "user_data",
			expected: "my_app_db.user_data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildPostgreSQLNamespace(tt.database, tt.schema)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestBuildOracleNamespace tests the Oracle namespace builder with priority logic.
func TestBuildOracleNamespace(t *testing.T) {
	tests := []struct {
		name        string
		serviceName string
		sid         string
		database    string
		expected    string
	}{
		{
			name:        "serviceNameOnly",
			serviceName: "PRODDB",
			sid:         "",
			database:    "",
			expected:    "PRODDB||",
		},
		{
			name:        "sidOnly",
			serviceName: "",
			sid:         "ORCL",
			database:    "",
			expected:    "|ORCL|",
		},
		{
			name:        "databaseOnly",
			serviceName: "",
			sid:         "",
			database:    "appdb",
			expected:    "||appdb",
		},
		{
			name:        "serviceNameTakesPrecedence",
			serviceName: "PRODDB",
			sid:         "ORCL",
			database:    "appdb",
			expected:    "PRODDB|ORCL|appdb",
		},
		{
			name:        "sidTakesPrecedenceOverDatabase",
			serviceName: "",
			sid:         "ORCL",
			database:    "appdb",
			expected:    "|ORCL|appdb",
		},
		{
			name:        "allEmpty",
			serviceName: "",
			sid:         "",
			database:    "",
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildOracleNamespace(tt.serviceName, tt.sid, tt.database)
			assert.Equal(t, tt.expected, result)
		})
	}
}
