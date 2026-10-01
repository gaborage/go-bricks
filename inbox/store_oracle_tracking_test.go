package inbox

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
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
	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// TestOracleDuplicatesThroughTrackedConnectionAreNotFailures runs each Oracle
// duplicate the stores detect by ORA-00001 through the tracking wrapper: the
// duplicate emits no ERROR line (request severity is never escalated) and its
// insert span keeps status Unset, while the stores still report the duplicate.
func TestOracleDuplicatesThroughTrackedConnectionAreNotFailures(t *testing.T) {
	exporter := enableDBSpans(t)

	tests := []struct {
		name   string
		script func(tx *dbtesting.TestTx)
		run    func(t *testing.T, ctx context.Context, tx dbtypes.Tx)
		insert string
	}{
		{
			name: "mark_processed_duplicate_delivery",
			script: func(tx *dbtesting.TestTx) {
				tx.ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnError(oracleUniqueViolation())
			},
			run: func(t *testing.T, ctx context.Context, tx dbtypes.Tx) {
				inserted, err := newOracleTestStore(t).MarkProcessed(ctx, tx, sampleRecord())
				require.NoError(t, err)
				assert.False(t, inserted, "the duplicate is still reported as processed-before")
			},
			insert: "INSERT INTO gobricks_inbox",
		},
		{
			name: "hold_park_row_duplicate",
			script: func(tx *dbtesting.TestTx) {
				tx.ExpectQuery(`FOR UPDATE`).WillReturnRows(dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
				tx.ExpectExec(`INSERT INTO ` + holdTable).WillReturnError(oracleUniqueViolation())
			},
			run: func(t *testing.T, ctx context.Context, tx dbtypes.Tx) {
				inserted, err := newOracleHoldTestStore(t).Park(ctx, tx, sampleHoldRow())
				require.NoError(t, err)
				assert.False(t, inserted, "a re-park is reported as not inserted")
			},
			insert: "INSERT INTO " + holdTable + " (",
		},
		{
			name: "hold_marker_insert_race",
			script: func(tx *dbtesting.TestTx) {
				tx.ExpectQuery(`FOR UPDATE`).WillReturnRows(dbtesting.NewRowSet("tenant_id"))
				tx.ExpectExec(`INSERT INTO ` + holdTenantTable).WillReturnError(oracleUniqueViolation())
			},
			run: func(t *testing.T, ctx context.Context, tx dbtypes.Tx) {
				// TestDB cannot script "empty, then present", so the race is lost twice
				// and the store gives up; what is pinned is that each lost insert is
				// tracked as expected.
				_, err := newOracleHoldTestStore(t).Park(ctx, tx, sampleHoldRow())
				require.Error(t, err)
			},
			insert: "INSERT INTO " + holdTenantTable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter.Reset()
			db := dbtesting.NewTestDB(dbtypes.Oracle)
			tt.script(db.ExpectTransaction())
			tracked := database.NewTrackedConnection(db, logger.New(testconsts.TestLoggerLevelDisabled, false), nil)

			var mu sync.Mutex
			var levels []zerolog.Level
			ctx := logger.WithSeverityHook(t.Context(), func(level zerolog.Level) {
				mu.Lock()
				defer mu.Unlock()
				levels = append(levels, level)
			})

			tx, err := tracked.Begin(ctx)
			require.NoError(t, err)
			tt.run(t, ctx, tx)

			mu.Lock()
			assert.Empty(t, levels, "a duplicate must not log ERROR or WARN")
			mu.Unlock()
			inserts := 0
			spans := exporter.GetSpans()
			for i := range spans {
				if spanQueryContains(&spans[i], tt.insert) {
					inserts++
					assert.Equal(t, codes.Unset, spans[i].Status.Code, "the duplicate's span is not failed")
				}
			}
			assert.Positive(t, inserts, "no span recorded for %q", tt.insert)
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

func spanQueryContains(s *tracetest.SpanStub, fragment string) bool {
	for _, kv := range s.Attributes {
		if kv.Key == attribute.Key("db.query.text") && strings.Contains(kv.Value.AsString(), fragment) {
			return true
		}
	}
	return false
}
