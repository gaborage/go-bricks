package server

import (
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// maskedPathTargets are spellings of one value under probeSkipModuleRoute that every path sink
// must render as probeSkipBase+"/REDACTED" (#2212).
var maskedPathTargets = []struct {
	name   string
	target string
}{
	{name: "plain_value", target: probeSkipBase + "/" + parityPAN},
	{name: "percent_encoded_value", target: probeSkipBase + "/%34" + parityPAN[1:]},
	{name: "encoded_slash", target: probeSkipBase + "/" + parityPAN[:8] + "%2F" + parityPAN[8:]},
	{name: "encoded_probe_spelling", target: probeSkipEncodedHlth},
}

// TestFrameworkLinesRenderTheMaskedPath is the #2212 L6p tracer bullet for the WARN and ERROR
// sinks: a PAN in a path segment, however spelled, prints <base>/REDACTED in each of the
// tenant-rejection WARN, the IP pre-guard WARN, the three forwarded-client-cert WARNs, the
// tenant-owned family's refusal ERROR and the panic-guard ERROR, and no byte run of the value
// reaches any of them.
func TestFrameworkLinesRenderTheMaskedPath(t *testing.T) {
	const masked = probeSkipBase + "/" + maskToken
	sinks := []struct {
		name  string
		level string
		emit  func(l logger.Logger, c *echo.Context)
	}{
		{name: "tenant_rejection", level: "warn", emit: func(l logger.Logger, c *echo.Context) { logTenantRejection(l, c, "empty tenant") }},
		{name: "ip_preguard", level: "warn", emit: logIPPreGuardRejection},
		{name: "fcc_missing_identity", level: "warn", emit: logForwardedClientCertRejection},
		{name: "fcc_duplicate_header", level: "warn", emit: func(l logger.Logger, c *echo.Context) {
			logForwardedClientCertDuplicateWarning(l, c, errors.New("duplicated header"))
		}},
		{name: "fcc_leaf_not_decoded", level: "warn", emit: func(l logger.Logger, c *echo.Context) {
			logForwardedClientCertLeafWarning(l, c, errors.New("bad leaf"), "leaf")
		}},
		{name: "tenant_family_refusal", level: "error", emit: func(l logger.Logger, c *echo.Context) {
			logTenantRefusal(newHandlerContext(c, nil), l, reasonDenyError)
		}},
		{name: "panic_guard", level: "error", emit: func(l logger.Logger, c *echo.Context) { reportPanic(l, c, "boom") }},
	}
	for _, sink := range sinks {
		for _, tc := range maskedPathTargets {
			t.Run(sink.name+"/"+tc.name, func(t *testing.T) {
				l := &testLogger{}
				e := newEngine()
				e.GET(probeSkipModuleRoute, func(c *echo.Context) error {
					sink.emit(l, c)
					return c.NoContent(http.StatusNoContent)
				})

				require.Equal(t, http.StatusNoContent, serveEngine(e, http.MethodGet, tc.target).Code)
				entries := l.logEntries()
				require.Len(t, entries, 1, "the sink writes one line")
				entry := entries[0]
				assert.Equal(t, sink.level, entry.level)
				if path, ok := entry.values["path"]; ok {
					// ip_preguard quotes its fields (logSafeValue); the panic guard does not.
					assert.Equal(t, masked, strings.Trim(path, `"`), "the structured path field")
				} else {
					assert.Contains(t, entry.msg, `path="`+masked+`"`, "the quoted path in the message")
				}
				for _, text := range append([]string{entry.msg}, slices.Collect(maps.Values(entry.values))...) {
					assert.NotContains(t, text, parityPAN[:8], "no PAN byte run may reach the line")
					assert.NotContains(t, text, parityPAN[8:], "no PAN byte run may reach the line")
				}
			})
		}
	}
}

// TestStartLogsOnePathRenderingLinePerEngine pins the ADR-167 startup line: each engine the
// server starts states the inbound-path rule once, naming its listener and the mask token,
// before that listener binds.
func TestStartLogsOnePathRenderingLinePerEngine(t *testing.T) {
	tests := []struct {
		name      string
		build     func(log *testLogger) *Server
		listeners []string
	}{
		{
			name:      "application_listener_only",
			build:     func(log *testLogger) *Server { return newServer(newProbeTestConfig(""), log) },
			listeners: []string{listenerApplication},
		},
		{
			name:      "with_probe_listener",
			build:     func(log *testLogger) *Server { return newProbeTestServer(newProbeTestConfig(""), log) },
			listeners: []string{listenerProbe, listenerApplication},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &testLogger{}
			srv := tt.build(log)
			errCh := startServer(srv)
			waitForServerReady(t, srv)
			shutdownAndDrain(t, srv, errCh)

			var lines []testLogEntry
			for _, e := range log.logEntries() {
				if strings.HasPrefix(e.msg, "[server.paths] ") {
					lines = append(lines, e)
				}
			}
			require.Len(t, lines, len(tt.listeners), "one line per engine")
			for i, listener := range tt.listeners {
				assert.Equal(t, "info", lines[i].level)
				assert.Equal(t,
					"[server.paths] inbound paths render from the matched route template rule=masked_route_template listener="+
						listener+" token=REDACTED",
					lines[i].msg)
			}
		})
	}
}
