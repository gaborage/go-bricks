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
				entry := emitOneLine(t, http.MethodGet, probeSkipModuleRoute, tc.target, sink.emit)
				assert.Equal(t, sink.level, entry.level)
				assertLineValue(t, entry, "path", masked)
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
// before that listener serves a request; the action type keeps it out of trace sampling.
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
			listeners: []string{ListenerProbes, listenerApplication},
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
				assert.Equal(t, "action", lines[i].values["log.type"])
				assert.Equal(t,
					"[server.paths] inbound paths render from the matched route template rule=masked_route_template listener="+
						listener+" token=REDACTED unmatched=REDACTED",
					lines[i].msg)
			}
		})
	}
}
