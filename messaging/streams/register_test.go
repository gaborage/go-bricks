package streams

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/streamruntime"
	"github.com/gaborage/go-bricks/logger"
)

const (
	collectMsg      = "Collecting module stream declarations"
	declaringModule = "declaring-module"
	silentModule    = "silent-module"
	plainModule     = "plain-module"
	brokenModule    = "broken-module"
)

// testDeclarer is a module that implements StreamDeclarer. A nil declare is the
// shape a config-gated declarer takes on a deployment where its feature is off:
// it asserts, and declares nothing.
type testDeclarer struct {
	name    string
	calls   int
	declare func(*Declarations)
}

func (m *testDeclarer) Name() string { return m.name }

func (m *testDeclarer) DeclareStreams(decls *Declarations) {
	m.calls++
	if m.declare != nil {
		m.declare(decls)
	}
}

// declareLopsided declares 4 streams (one of them a super stream), 2 consumers and
// 3 publishers, so no two of the four per-module counts can be swapped for one
// another without the assertions noticing.
func declareLopsided(decls *Declarations) {
	decls.DeclareStream("orders", nil)
	decls.DeclareStream("payments", nil)
	decls.DeclareStream("refunds", nil)
	decls.DeclareSuperStream("events", 2, nil)
	decls.DeclareConsumer(&ConsumerOptions{Stream: "orders", Name: "orders-worker", Handler: noopHandler})
	decls.DeclareConsumer(&ConsumerOptions{Stream: "payments", Name: "payments-worker", Handler: noopHandler})
	decls.DeclarePublisher(&PublisherOptions{Stream: "orders"})
	decls.DeclarePublisher(&PublisherOptions{Stream: "refunds"})
	decls.DeclareSuperStreamPublisher(&SuperStreamPublisherOptions{SuperStream: "events"})
}

// declareOrphanConsumer declares a consumer whose target stream was never
// declared, the failure Validate reports.
func declareOrphanConsumer(decls *Declarations) {
	decls.DeclareConsumer(&ConsumerOptions{Stream: "missing", Name: "orphan", Handler: noopHandler})
}

// plainTestModule does not implement StreamDeclarer at all.
type plainTestModule struct{ name string }

func (m *plainTestModule) Name() string { return m.name }

// captureCollectLogs returns the JSON log lines CollectDeclarations wrote, reusing
// the package's stdout capture: the framework logger binds stdout at construction,
// so it is built inside fn.
func captureCollectLogs(t *testing.T, modules []streamruntime.ModuleNamer) (lines []map[string]any, err error) {
	t.Helper()

	out := captureReplayLogs(t, func() {
		_, err = streamRuntime{}.CollectDeclarations(modules, logger.New("info", false))
	})

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), "log line is not JSON: %s", line)
		lines = append(lines, entry)
	}
	return lines, err
}

// perModuleLine returns the per-module collection line for the named module, or
// nil when the module contributed no line at all.
func perModuleLine(entries []map[string]any, module string) map[string]any {
	for _, entry := range entries {
		if entry["message"] == collectMsg && entry["module"] == module {
			return entry
		}
	}
	return nil
}

func assertCounts(t *testing.T, line map[string]any, streams, superStreams, consumers, publishers float64) {
	t.Helper()
	assert.Equal(t, streams, line["streams"], "streams count")
	assert.Equal(t, superStreams, line["superstreams"], "superstreams count")
	assert.Equal(t, consumers, line["consumers"], "consumers count")
	assert.Equal(t, publishers, line["publishers"], "publishers count")
}

func TestCollectDeclarationsAttributesCountsPerModule(t *testing.T) {
	declaring := &testDeclarer{name: declaringModule, declare: declareLopsided}
	silent := &testDeclarer{name: silentModule}
	plain := &plainTestModule{name: plainModule}

	entries, err := captureCollectLogs(t, []streamruntime.ModuleNamer{declaring, silent, plain})
	require.NoError(t, err)

	declaringLine := perModuleLine(entries, declaringModule)
	require.NotNil(t, declaringLine, "declaring module has a per-module line")
	assertCounts(t, declaringLine, 4, 1, 2, 3)

	silentLine := perModuleLine(entries, silentModule)
	require.NotNil(t, silentLine, "a declarer that declares nothing is still reported")
	assertCounts(t, silentLine, 0, 0, 0, 0)

	assert.Nil(t, perModuleLine(entries, plainModule), "a module that does not implement StreamDeclarer contributes no line")

	assert.Equal(t, 1, declaring.calls, "DeclareStreams is called exactly once per declarer")
	assert.Equal(t, 1, silent.calls, "DeclareStreams is called exactly once per declarer")
}

func TestCollectDeclarationsLeavesAggregateLineUnchanged(t *testing.T) {
	entries, err := captureCollectLogs(t, []streamruntime.ModuleNamer{&testDeclarer{name: declaringModule, declare: declareLopsided}})
	require.NoError(t, err)

	var aggregate map[string]any
	for _, entry := range entries {
		if entry["message"] == "Stream declarations collected and validated successfully" {
			aggregate = entry
		}
	}
	require.NotNil(t, aggregate, "aggregate line is emitted")
	assert.Equal(t, float64(4), aggregate["streams"])
	assert.Equal(t, float64(2), aggregate["consumers"])
	assert.NotContains(t, aggregate, "publishers", "the aggregate line keeps its existing fields")
}

func TestCollectDeclarationsReportsValidationFailure(t *testing.T) {
	entries, err := captureCollectLogs(t, []streamruntime.ModuleNamer{&testDeclarer{name: brokenModule, declare: declareOrphanConsumer}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream declaration validation failed")

	line := perModuleLine(entries, brokenModule)
	require.NotNil(t, line, "the offending module is still attributed")
	assertCounts(t, line, 0, 0, 1, 0)
}
