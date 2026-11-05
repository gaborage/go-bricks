package server

import (
	goerrors "errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/multitenant"
)

// pathTenantCheckConfig is a startable config under resolverType ("" leaves multitenant off).
func pathTenantCheckConfig(resolverType string) *config.Config {
	cfg := newProbeTestConfig("")
	if resolverType == "" {
		return cfg
	}
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = resolverType
	cfg.Multitenant.Resolver.Path.Segment = 1
	if resolverType == config.ResolverTypeComposite {
		cfg.Multitenant.Resolver.Order = []string{config.ResolverTypeHeader, config.ResolverTypePath}
	}
	return cfg
}

// TestStartRefusesPathTenancyWithoutTenantChecker pins ADR-170's wiring refusal: a server whose
// tenant resolver may read the path and that has no TenantChecker refuses Start before binding,
// where it would answer 400 to every path tenant; a resolver that reads no path, or multitenant
// off, needs no check.
func TestStartRefusesPathTenancyWithoutTenantChecker(t *testing.T) {
	tests := []struct {
		name     string
		resolver string
		refused  bool
	}{
		{name: "path_resolver", resolver: config.ResolverTypePath, refused: true},
		{name: "composite_with_path", resolver: config.ResolverTypeComposite, refused: true},
		{name: "header_resolver", resolver: config.ResolverTypeHeader},
		{name: "multitenant_off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewWithOptions(pathTenantCheckConfig(tt.resolver), &testLogger{}, Options{})
			require.Equal(t, tt.refused, goerrors.Is(srv.setupErr, errNoTenantChecker))
			if !tt.refused {
				return
			}
			require.ErrorIs(t, srv.Start(), errNoTenantChecker)
			assert.Nil(t, srv.boundAddr.Load(), "refused before binding")
		})
	}
}

// TestStartLogsPathTenantCheckLine pins ADR-170's startup line: a server whose seat asks a
// TenantChecker about a path-read tenant states it once at Start, as an action line naming the
// store's type and, for the built-in *config.TenantStore, the tenants it holds (a named-database
// key is no tenant); a seat that reads no path writes none.
func TestStartLogsPathTenantCheckLine(t *testing.T) {
	const prefix = "[server.tenant] a tenant read from the request path is written only when " +
		"the tenant store holds it (ADR-170) store="
	store := config.NewTenantStore(&config.Config{})
	store.AddTenant("acme", &config.TenantEntry{})
	store.AddTenant("globex", &config.TenantEntry{})
	store.AddTenant(config.NamedDatabasePrefix+"reports", &config.TenantEntry{})
	tests := []struct {
		name     string
		resolver string
		checker  multitenant.TenantChecker
		want     string // the line; "" for none
	}{
		{name: "built_in_store", resolver: config.ResolverTypePath, checker: store, want: prefix + "*config.TenantStore tenants=2"},
		{name: "custom_checker", resolver: config.ResolverTypeComposite, checker: testutil.TenantSet{"acme": {}}, want: prefix + "testutil.TenantSet"},
		{name: "header_resolver", resolver: config.ResolverTypeHeader, checker: store},
		{name: "multitenant_off", checker: store},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &testLogger{}
			srv := NewWithOptions(pathTenantCheckConfig(tt.resolver), log, Options{TenantChecker: tt.checker})
			errCh := startServer(srv)
			waitForServerReady(t, srv)
			shutdownAndDrain(t, srv, errCh)

			var lines []testLogEntry
			for _, e := range log.logEntries() {
				if strings.HasPrefix(e.msg, "[server.tenant] a tenant read") {
					lines = append(lines, e)
				}
			}
			if tt.want == "" {
				assert.Empty(t, lines)
				return
			}
			require.Len(t, lines, 1)
			assert.Equal(t, "info", lines[0].level)
			assert.Equal(t, "action", lines[0].values["log.type"])
			assert.Equal(t, tt.want, lines[0].msg)
		})
	}
}
