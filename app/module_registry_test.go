package app

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
	"github.com/gaborage/go-bricks/server"
)

// recLogger is a recording logger.Logger that captures each event's level, Str
// fields, Err text and terminal Msg, so tests can assert emissions without
// swapping the process-global os.Stdout.
type recLogger struct {
	mu     sync.Mutex
	events []recEvent
}

type recEvent struct {
	l     *recLogger
	str   map[string]string
	dur   map[string]time.Duration
	level string
	err   string
	msg   string
}

func (l *recLogger) event(level string) logger.LogEvent {
	return &recEvent{l: l, level: level, str: map[string]string{}, dur: map[string]time.Duration{}}
}
func (l *recLogger) Info() logger.LogEvent                     { return l.event("info") }
func (l *recLogger) Error() logger.LogEvent                    { return l.event("error") }
func (l *recLogger) Debug() logger.LogEvent                    { return l.event("debug") }
func (l *recLogger) Warn() logger.LogEvent                     { return l.event("warn") }
func (l *recLogger) Fatal() logger.LogEvent                    { return l.event("fatal") }
func (l *recLogger) WithContext(_ any) logger.Logger           { return l }
func (l *recLogger) WithFields(_ map[string]any) logger.Logger { return l }

func (l *recLogger) routeRegisteredLines() []recEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []recEvent
	for _, e := range l.events {
		if e.msg == "Route registered" {
			out = append(out, e)
		}
	}
	return out
}

func (e *recEvent) Msg(msg string) {
	e.msg = msg
	e.l.mu.Lock()
	e.l.events = append(e.l.events, *e)
	e.l.mu.Unlock()
}
func (e *recEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }
func (e *recEvent) Str(k, v string) logger.LogEvent { e.str[k] = v; return e }
func (e *recEvent) Err(err error) logger.LogEvent {
	if err != nil {
		e.err = err.Error()
	}
	return e
}
func (e *recEvent) Int(_ string, _ int) logger.LogEvent           { return e }
func (e *recEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *recEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *recEvent) Dur(k string, v time.Duration) logger.LogEvent { e.dur[k] = v; return e }
func (e *recEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
func (e *recEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }
func (e *recEvent) Bool(_ string, _ bool) logger.LogEvent         { return e }
func (e *recEvent) Enabled() bool                                 { return true }

// fakeRouteModule registers its descriptors straight into DefaultRouteRegistry
// (the delta only watches Count()/Routes(), so this faithfully models the typed
// and raw registration paths from RegisterRoutes' perspective).
type fakeRouteModule struct {
	name   string
	routes []server.RouteDescriptor
}

func (m *fakeRouteModule) Name() string             { return m.name }
func (m *fakeRouteModule) Init(_ *ModuleDeps) error { return nil }
func (m *fakeRouteModule) Shutdown() error          { return nil }
func (m *fakeRouteModule) RegisterRoutes(_ *server.HandlerRegistry, _ server.RouteRegistrar) {
	for i := range m.routes {
		server.DefaultRouteRegistry.Register(&m.routes[i])
	}
}

func newRouteLogRegistry(t *testing.T, env string, logRoutes *bool, mods ...Module) (*ModuleRegistry, *recLogger) {
	t.Helper()
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	rec := &recLogger{}
	cfg := &config.Config{}
	cfg.App.Env = env
	cfg.Server.LogRoutes = logRoutes
	reg := NewModuleRegistry(&ModuleDeps{Logger: rec, Config: cfg})
	for _, m := range mods {
		require.NoError(t, reg.Register(m))
	}
	return reg, rec
}

func TestRegisterRoutesLogsInDevelopment(t *testing.T) {
	reg, rec := newRouteLogRegistry(t, "development", nil,
		&fakeRouteModule{name: "users", routes: []server.RouteDescriptor{
			{Method: "GET", Path: "/v1/users"},
			{Method: "POST", Path: "/v1/users"},
		}})
	reg.RegisterRoutes(nil)
	lines := rec.routeRegisteredLines()
	require.Len(t, lines, 2)
	assert.Equal(t, "users", lines[0].str["module"])
	assert.Equal(t, "GET", lines[0].str["method"])
	assert.Equal(t, "/v1/users", lines[0].str["path"])
	assert.Equal(t, "POST", lines[1].str["method"])
}

func TestRegisterRoutesSilentInProduction(t *testing.T) {
	reg, rec := newRouteLogRegistry(t, "production", nil,
		&fakeRouteModule{name: "users", routes: []server.RouteDescriptor{{Method: "GET", Path: "/v1/users"}}})
	reg.RegisterRoutes(nil)
	assert.Empty(t, rec.routeRegisteredLines())
}

func TestRegisterRoutesExplicitFalseSilentInDevelopment(t *testing.T) {
	reg, rec := newRouteLogRegistry(t, "development", new(false),
		&fakeRouteModule{name: "users", routes: []server.RouteDescriptor{{Method: "GET", Path: "/v1/users"}}})
	reg.RegisterRoutes(nil)
	assert.Empty(t, rec.routeRegisteredLines())
}

func TestRegisterRoutesExplicitTrueEmitsInProduction(t *testing.T) {
	reg, rec := newRouteLogRegistry(t, "production", new(true),
		&fakeRouteModule{name: "users", routes: []server.RouteDescriptor{{Method: "GET", Path: "/v1/users"}}})
	reg.RegisterRoutes(nil)
	require.Len(t, rec.routeRegisteredLines(), 1)
}

func TestRegisterRoutesAttributesRoutesToRegisteringModule(t *testing.T) {
	reg, rec := newRouteLogRegistry(t, "development", nil,
		&fakeRouteModule{name: "users", routes: []server.RouteDescriptor{{Method: "GET", Path: "/v1/users"}}},
		&fakeRouteModule{name: "orders", routes: []server.RouteDescriptor{
			{Method: "GET", Path: "/v1/orders"},
			{Method: "POST", Path: "/v1/orders"},
		}})
	reg.RegisterRoutes(nil)
	lines := rec.routeRegisteredLines()
	require.Len(t, lines, 3)
	assert.Equal(t, "users", lines[0].str["module"])
	assert.Equal(t, "orders", lines[1].str["module"])
	assert.Equal(t, "orders", lines[2].str["module"])
}

func TestCollectRouteLogEntriesAttributesRawAndTypedRoutes(t *testing.T) {
	// Attribution is purely positional: RouteDescriptor.ModuleName is empty for
	// every route (nothing calls server.WithModule), so the module is derived
	// from the registration-order span, not the descriptor field.
	routes := []server.RouteDescriptor{
		{Method: "GET", Path: "/_sys/debug"}, // framework span
		{Method: "GET", Path: "/v1/users"},   // modA, typed
		{Method: "POST", Path: "/v1/users"},  // modA, raw
		{Method: "GET", Path: "/v1/orders"},  // modB
	}
	spans := []routeSpan{
		{module: "framework", start: 0},
		{module: "modA", start: 1},
		{module: "modB", start: 3},
	}
	got := collectRouteLogEntries(spans, routes)
	require.Len(t, got, 4)
	assert.Equal(t, routeLogEntry{module: "framework", method: "GET", path: "/_sys/debug"}, got[0])
	assert.Equal(t, routeLogEntry{module: "modA", method: "GET", path: "/v1/users"}, got[1])
	assert.Equal(t, routeLogEntry{module: "modA", method: "POST", path: "/v1/users"}, got[2], "raw route attributed to modA, not empty")
	assert.Equal(t, routeLogEntry{module: "modB", method: "GET", path: "/v1/orders"}, got[3])
}

func TestCollectRouteLogEntriesZeroRouteModule(t *testing.T) {
	// A module that registers no routes contributes no entries and does not steal
	// the next module's routes.
	routes := []server.RouteDescriptor{{Method: "GET", Path: "/a"}}
	spans := []routeSpan{{module: "modA", start: 0}, {module: "modB", start: 1}} // modB registered none
	got := collectRouteLogEntries(spans, routes)
	require.Len(t, got, 1)
	assert.Equal(t, "modA", got[0].module)
}

func TestCollectRouteLogEntriesFrameworkLeadingSpan(t *testing.T) {
	routes := []server.RouteDescriptor{{Method: "GET", Path: "/_sys/gc"}, {Method: "GET", Path: "/v1/x"}}
	spans := []routeSpan{{module: "framework", start: 0}, {module: "modA", start: 1}}
	got := collectRouteLogEntries(spans, routes)
	require.Len(t, got, 2)
	assert.Equal(t, "framework", got[0].module)
	assert.Equal(t, "modA", got[1].module)
}

func TestCollectRouteLogEntriesDefensiveOutOfRange(t *testing.T) {
	// A span.start beyond the snapshot (should be impossible single-threaded) is
	// skipped, never panics.
	routes := []server.RouteDescriptor{{Method: "GET", Path: "/a"}}
	spans := []routeSpan{{module: "framework", start: 0}, {module: "bad", start: 99}}
	assert.NotPanics(t, func() {
		got := collectRouteLogEntries(spans, routes)
		assert.Len(t, got, 1) // only the valid framework span resolves
	})
}

// queueConflictModule declares one queue name twice with shapes that cannot
// merge — the cross-module collision reduced to a single module.
type queueConflictModule struct {
	name  string
	queue string
}

func (m *queueConflictModule) Name() string             { return m.name }
func (m *queueConflictModule) Init(_ *ModuleDeps) error { return nil }
func (m *queueConflictModule) Shutdown() error          { return nil }
func (m *queueConflictModule) DeclareMessaging(decls *messaging.Declarations) {
	decls.RegisterQueue(&messaging.QueueDeclaration{Name: m.queue, Durable: true})
	decls.RegisterQueue(&messaging.QueueDeclaration{Name: m.queue, Durable: false})
}

// TestDeclareMessagingFailsOnConflictingQueueDeclarations pins that a queue
// conflict reaches the fatal startup path rather than being silently merged or
// dropped: DeclareMessaging is what app.buildMessagingDeclarations calls.
func TestDeclareMessagingFailsOnConflictingQueueDeclarations(t *testing.T) {
	const queue = "orders.events.queue"
	reg := NewModuleRegistry(&ModuleDeps{Logger: &recLogger{}, Config: &config.Config{}})
	require.NoError(t, reg.Register(&queueConflictModule{name: "orders", queue: queue}))

	err := reg.DeclareMessaging(messaging.NewDeclarations())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "declaration validation failed")
	assert.Contains(t, err.Error(), "conflicting queue declarations (1 conflict(s))")
	assert.Contains(t, err.Error(), queue)
	// The whole detail line, not just the field name: asserting "Durable" alone
	// would pass if the values were dropped, or if kept and rejected were swapped.
	assert.Contains(t, err.Error(), `Durable kept "true" vs rejected "false"`)
}

// fakeDBRequiringModule declares DatabaseRequirer with a configurable verdict and
// records whether Init ran, so a test can assert the guard fires BEFORE Init rather
// than merely alongside it.
type fakeDBRequiringModule struct {
	name     string
	requires bool
	inited   bool
}

func (m *fakeDBRequiringModule) Name() string             { return m.name }
func (m *fakeDBRequiringModule) Init(_ *ModuleDeps) error { m.inited = true; return nil }
func (m *fakeDBRequiringModule) Shutdown() error          { return nil }
func (m *fakeDBRequiringModule) RequiresDatabase() bool   { return m.requires }

func newDBRequirementRegistry(rootDBAbsent bool) *ModuleRegistry {
	reg := NewModuleRegistry(&ModuleDeps{Logger: &recLogger{}, Config: &config.Config{}})
	reg.rootDBAbsent = rootDBAbsent
	return reg
}

func TestRegisterRejectsDatabaseRequirerWhenDatabaseAbsent(t *testing.T) {
	mod := &fakeDBRequiringModule{name: "payments", requires: true}

	err := newDBRequirementRegistry(true).Register(mod)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "payments", "the error must name the offending module")
	// This fatal must NOT read as the framework's benign "intentionally absent" marker.
	// A caller using the framework's own skip-and-degrade idiom
	// (err != nil && !config.IsNotConfigured(err)) would otherwise swallow the abort and
	// serve without the module's routes and global middleware.
	assert.False(t, config.IsNotConfigured(err))
	var cfgErr *config.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "missing", cfgErr.Category)
	// The guard's whole point is that the module never runs without the dependency it
	// declared as mandatory — asserting only the error would pass if Init ran first.
	assert.False(t, mod.inited, "Init must not run when the requirement is unmet")
}

func TestRegisterAcceptsModulesWhenDatabaseRequirementDoesNotApply(t *testing.T) {
	tests := []struct {
		name         string
		module       Module
		rootDBAbsent bool
	}{
		{
			name: "requirer_with_database_present", rootDBAbsent: false,
			module: &fakeDBRequiringModule{name: "payments", requires: true},
		},
		// A module may implement the interface and still decline, gating the
		// requirement on its own construction-time config.
		{
			name: "requirer_declines_requirement", rootDBAbsent: true,
			module: &fakeDBRequiringModule{name: "payments", requires: false},
		},
		{
			name: "module_never_declares_requirement", rootDBAbsent: true,
			module: &minimalModule{name: "forwarder"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, newDBRequirementRegistry(tt.rootDBAbsent).Register(tt.module))
		})
	}
}

func TestModuleRegistryDeclareStreamsCollectsFromDeclarers(t *testing.T) {
	log := logger.New("error", false)
	registry := NewModuleRegistry(&ModuleDeps{Logger: log, Config: &config.Config{}})
	declarer := &streamModule{name: "orders", declaration: declareOneConsumer}
	require.NoError(t, registry.Register(declarer))
	require.NoError(t, registry.Register(&minimalModule{name: "plain"}))

	decls := streams.NewDeclarations()
	require.NoError(t, registry.DeclareStreams(decls))

	assert.Equal(t, 1, declarer.calls, "a declarer is invoked exactly once")
	assert.Equal(t, streams.Stats{Streams: 1, Consumers: 1}, decls.Stats())
}

func TestModuleRegistryDeclareStreamsRejectsNilStore(t *testing.T) {
	registry := NewModuleRegistry(&ModuleDeps{Logger: logger.New("error", false), Config: &config.Config{}})

	err := registry.DeclareStreams(nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream declarations store is nil")
}

func TestModuleRegistryDeclareStreamsFailsOnInvalidDeclarations(t *testing.T) {
	log := logger.New("error", false)
	registry := NewModuleRegistry(&ModuleDeps{Logger: log, Config: &config.Config{}})
	require.NoError(t, registry.Register(&streamModule{name: "orders", declaration: func(decls *streams.Declarations) {
		decls.DeclareConsumer(&streams.ConsumerOptions{Stream: "ghost", Name: testStreamConsumer})
	}}))

	err := registry.DeclareStreams(streams.NewDeclarations())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream declaration validation failed")
	assert.Contains(t, err.Error(), "references undeclared stream")
}

func TestModuleRegistryDeclareStreamsWithNoDeclarersIsEmpty(t *testing.T) {
	log := logger.New("error", false)
	registry := NewModuleRegistry(&ModuleDeps{Logger: log, Config: &config.Config{}})
	require.NoError(t, registry.Register(&minimalModule{name: "plain"}))

	decls := streams.NewDeclarations()
	require.NoError(t, registry.DeclareStreams(decls))

	assert.True(t, decls.IsEmpty())
}

// warnLines returns every recorded warn-level event.
func (l *recLogger) warnLines() []recEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []recEvent
	for _, e := range l.events {
		if e.level == "warn" {
			out = append(out, e)
		}
	}
	return out
}
