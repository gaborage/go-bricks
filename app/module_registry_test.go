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
	"github.com/gaborage/go-bricks/server"
)

func routeLogPtr(b bool) *bool { return &b }

// recLogger is a recording logger.Logger that captures each event's Str fields
// and terminal Msg, for asserting the route-registered emission.
type recLogger struct {
	mu     sync.Mutex
	events []recEvent
}

type recEvent struct {
	l   *recLogger
	str map[string]string
	msg string
}

func (l *recLogger) event() logger.LogEvent                    { return &recEvent{l: l, str: map[string]string{}} }
func (l *recLogger) Info() logger.LogEvent                     { return l.event() }
func (l *recLogger) Error() logger.LogEvent                    { return l.event() }
func (l *recLogger) Debug() logger.LogEvent                    { return l.event() }
func (l *recLogger) Warn() logger.LogEvent                     { return l.event() }
func (l *recLogger) Fatal() logger.LogEvent                    { return l.event() }
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
func (e *recEvent) Msgf(format string, args ...any)               { e.Msg(fmt.Sprintf(format, args...)) }
func (e *recEvent) Str(k, v string) logger.LogEvent               { e.str[k] = v; return e }
func (e *recEvent) Err(_ error) logger.LogEvent                   { return e }
func (e *recEvent) Int(_ string, _ int) logger.LogEvent           { return e }
func (e *recEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *recEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *recEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
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
	reg, rec := newRouteLogRegistry(t, "development", routeLogPtr(false),
		&fakeRouteModule{name: "users", routes: []server.RouteDescriptor{{Method: "GET", Path: "/v1/users"}}})
	reg.RegisterRoutes(nil)
	assert.Empty(t, rec.routeRegisteredLines())
}

func TestRegisterRoutesExplicitTrueEmitsInProduction(t *testing.T) {
	reg, rec := newRouteLogRegistry(t, "production", routeLogPtr(true),
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
