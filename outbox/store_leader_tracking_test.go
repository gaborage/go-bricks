package outbox

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	oranet "github.com/sijms/go-ora/v2/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	msgTrackedExpected = "Database operation returned an expected error"
	msgTrackedError    = "Database operation error"
)

// leaderCase is one vendor's store with the lock error a non-leader replica gets.
type leaderCase struct {
	name    string
	vendor  string
	store   func(t *testing.T) Store
	lockErr error
}

func leaderCases() []leaderCase {
	return []leaderCase{
		{
			name:    "postgres_55p03",
			vendor:  dbtypes.PostgreSQL,
			store:   func(t *testing.T) Store { return newPostgresTestStore(t) },
			lockErr: &pgconn.PgError{Code: "55P03", Message: "could not obtain lock on row in relation"},
		},
		{
			name:    "oracle_ora_00054",
			vendor:  dbtypes.Oracle,
			store:   func(t *testing.T) Store { return newOracleTestStore(t) },
			lockErr: &oranet.OracleError{ErrCode: 54},
		},
	}
}

func levelsOf(lines []string, level string) []string {
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, level+" ") {
			out = append(out, line)
		}
	}
	return out
}

// TestLeadNotLeaderThroughTrackedConnectionLogsDebug runs a non-leader Lead through
// the tracking wrapper the framework actually hands the relay: the lock statement's
// declared lock-not-available logs at DEBUG, no ERROR line is emitted, and the
// replica still gets ErrNotLeader (ADR-088).
func TestLeadNotLeaderThroughTrackedConnectionLogsDebug(t *testing.T) {
	for _, tc := range leaderCases() {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtesting.NewTestDB(tc.vendor)
			tx := db.ExpectTransaction()
			tx.ExpectQuery(`FOR UPDATE NOWAIT`).WillReturnError(tc.lockErr)
			rec := newRecordingLogger()

			lead, err := tc.store(t).Lead(t.Context(), database.NewTrackedConnection(db, rec, nil))

			require.ErrorIs(t, err, ErrNotLeader)
			assert.Nil(t, lead)
			dbtesting.AssertRolledBack(t, tx)
			lines := rec.leveled()
			assert.Empty(t, levelsOf(lines, "ERROR"), "a non-leader must not log ERROR: %v", lines)
			assert.Empty(t, levelsOf(lines, "WARN"), "a non-leader must not log WARN: %v", lines)
			assert.Contains(t, lines, "DEBUG "+msgTrackedExpected)
		})
	}
}

// TestLeadFailingProbeThroughTrackedConnectionStaysLoud is the scope pin: only the
// lock statement carries the declaration, so Begin and Rollback track as before and
// a failing leadership probe still logs ERROR.
func TestLeadFailingProbeThroughTrackedConnectionStaysLoud(t *testing.T) {
	for _, tc := range leaderCases() {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtesting.NewTestDB(tc.vendor)
			tx := db.ExpectTransaction()
			tx.ExpectQuery(`FOR UPDATE NOWAIT`).WillReturnRows(dbtesting.NewRowSet("id").AddRow(int64(1)))
			// The probe fails with the very code the lock statement declares: outside the
			// lock statement it is a real failure.
			tx.ExpectExec(`SELECT 1`).WillReturnError(tc.lockErr)
			rec := newRecordingLogger()

			lead, err := tc.store(t).Lead(t.Context(), database.NewTrackedConnection(db, rec, nil))
			require.NoError(t, err)
			probeErr := lead.Probe(t.Context())
			require.NoError(t, lead.Release(t.Context()))

			require.ErrorIs(t, probeErr, tc.lockErr)
			lines := rec.leveled()
			assert.Equal(t, []string{"ERROR " + msgTrackedError}, levelsOf(lines, "ERROR"))
			assert.NotContains(t, lines, "DEBUG "+msgTrackedExpected)
		})
	}
}

// TestLeadNotLeaderLockSpanStaysUnset: with observability enabled, the lock
// statement's span keeps status Unset for the declared error, while a probe failing
// with the same code still marks its span Error.
func TestLeadNotLeaderLockSpanStaysUnset(t *testing.T) {
	exporter := enableDBSpans(t)

	for _, tc := range leaderCases() {
		t.Run(tc.name, func(t *testing.T) {
			exporter.Reset()
			db := dbtesting.NewTestDB(tc.vendor)
			tx := db.ExpectTransaction()
			tx.ExpectQuery(`FOR UPDATE NOWAIT`).WillReturnError(tc.lockErr)

			_, err := tc.store(t).Lead(t.Context(), database.NewTrackedConnection(db, newRecordingLogger(), nil))
			require.ErrorIs(t, err, ErrNotLeader)

			lock := spanWithQuery(t, exporter.GetSpans(), "FOR UPDATE NOWAIT")
			assert.Equal(t, codes.Unset, lock.Status.Code)
			assert.Empty(t, lock.Events, "a declared-expected error must not record an exception event")
		})
		t.Run(tc.name+"_probe_marks_error", func(t *testing.T) {
			exporter.Reset()
			db := dbtesting.NewTestDB(tc.vendor)
			tx := db.ExpectTransaction()
			tx.ExpectQuery(`FOR UPDATE NOWAIT`).WillReturnRows(dbtesting.NewRowSet("id").AddRow(int64(1)))
			tx.ExpectExec(`SELECT 1`).WillReturnError(tc.lockErr)

			lead, err := tc.store(t).Lead(t.Context(), database.NewTrackedConnection(db, newRecordingLogger(), nil))
			require.NoError(t, err)
			require.Error(t, lead.Probe(t.Context()))
			require.NoError(t, lead.Release(t.Context()))

			probe := spanWithQuery(t, exporter.GetSpans(), "SELECT 1")
			assert.Equal(t, codes.Error, probe.Status.Code)
		})
	}
}

// enableDBSpans installs an in-memory tracer provider and turns database span
// emission on for the test, restoring both afterward. Callers must not be parallel.
func enableDBSpans(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	database.SetObservabilityEnabled(true)
	t.Cleanup(func() {
		database.SetObservabilityEnabled(false)
		otel.SetTracerProvider(original)
		_ = tp.Shutdown(context.Background())
	})
	return exporter
}

func spanWithQuery(t *testing.T, spans tracetest.SpanStubs, fragment string) tracetest.SpanStub {
	t.Helper()
	for i := range spans {
		for _, kv := range spans[i].Attributes {
			if kv.Key == attribute.Key("db.query.text") && strings.Contains(kv.Value.AsString(), fragment) {
				return spans[i]
			}
		}
	}
	require.Fail(t, "no span for statement", "fragment %q among %d spans", fragment, len(spans))
	return tracetest.SpanStub{}
}
