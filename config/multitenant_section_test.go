package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateMultitenantTenantsCacheDefaults proves that an enabled tenant
// cache with a host but no port/poolsize is hardened at startup: Redis
// defaults (port 6379, poolsize 10, timeouts) are applied and persisted back
// to the tenants map, exactly as already done for tenant.Database. Without the
// fix, normalizeMultitenantTenants never touches tenant.Cache, so the raw
// zero-value Redis config reaches the cache client and fails at first request
// instead of at startup.
func TestValidateMultitenantTenantsCacheDefaults(t *testing.T) {
	cfg := &Config{
		App:    createValidAppConfig(),
		Server: createValidServerConfig(),
		Log:    createValidLogConfig(),
		Multitenant: MultitenantConfig{
			Enabled: true,
			Resolver: ResolverConfig{
				Type:   "header",
				Header: testTenantHeader,
			},
			Tenants: map[string]TenantEntry{
				"acme": {
					Database: DatabaseConfig{
						Type:     PostgreSQL,
						Host:     "acme.db",
						Port:     5432,
						Database: "acme",
						Username: "acme_user",
					},
					Cache: CacheConfig{
						Enabled: true,
						// Type, Port and PoolSize intentionally left at zero
						// values: there are no koanf defaults for per-tenant
						// cache keys, so validation must apply them itself.
						Redis: RedisConfig{Host: "acme.redis"},
					},
				},
			},
		},
		Source: SourceConfig{Type: SourceTypeStatic},
	}

	require.NoError(t, Validate(cfg))

	tenant := cfg.Multitenant.Tenants["acme"]
	assert.Equal(t, CacheTypeRedis, tenant.Cache.Type,
		"tenant cache type must default to redis via Validate wiring")
	assert.Equal(t, 6379, tenant.Cache.Redis.Port,
		"tenant cache without explicit port must default to 6379 and persist to the tenants map")
	assert.Equal(t, 10, tenant.Cache.Redis.PoolSize,
		"tenant cache without explicit poolsize must default to 10 and persist to the tenants map")
}

// TestValidateMultitenantTenantsCacheMisconfigFailsFast proves the HARDEN
// posture: a genuinely misconfigured tenant cache (enabled but no host) is
// rejected at startup, not deferred to the first per-request cache access.
func TestValidateMultitenantTenantsCacheMisconfigFailsFast(t *testing.T) {
	cfg := &Config{
		App:    createValidAppConfig(),
		Server: createValidServerConfig(),
		Log:    createValidLogConfig(),
		Multitenant: MultitenantConfig{
			Enabled: true,
			Resolver: ResolverConfig{
				Type:   "header",
				Header: testTenantHeader,
			},
			Tenants: map[string]TenantEntry{
				"acme": {
					Database: DatabaseConfig{
						Type:     PostgreSQL,
						Host:     "acme.db",
						Port:     5432,
						Database: "acme",
						Username: "acme_user",
					},
					Cache: CacheConfig{
						Enabled: true,
						// Host omitted: must fail fast at startup.
					},
				},
			},
		},
		Source: SourceConfig{Type: SourceTypeStatic},
	}

	err := Validate(cfg)
	require.Error(t, err, "enabled tenant cache without a host must fail at startup")
	assert.Contains(t, err.Error(), "cache.redis.host")
}

// normalizeTenantsAndCheckMultitenant runs the tenant half of normalize before
// checkMultitenant: check assumes normalize already ran (per-tenant cache
// defaults included), and these fixtures are hand-built. Only the tenant loop
// runs — the resolver/limits fills would change what the failure tables assert.
func normalizeTenantsAndCheckMultitenant(t *testing.T, mt *MultitenantConfig, db *DatabaseConfig, msg *MessagingConfig, source *SourceConfig) error {
	t.Helper()
	require.NoError(t, normalizeMultitenantTenants(mt.Tenants))
	return checkMultitenant(mt, db, msg, source)
}

func TestValidateMultitenantDisabled(t *testing.T) {
	mtConfig := &MultitenantConfig{
		Enabled: false,
	}
	dbConfig := &DatabaseConfig{
		Type: PostgreSQL,
		Host: "localhost",
		Port: 5432,
	}
	msgConfig := &MessagingConfig{
		Broker: BrokerConfig{
			URL: testAMQPHost,
		},
	}

	sourceConfig := &SourceConfig{Type: SourceTypeStatic}
	err := checkMultitenant(mtConfig, dbConfig, msgConfig, sourceConfig)
	assert.NoError(t, err, "Validation should pass when multitenant is disabled")
}

func TestValidateMultitenantSuccess(t *testing.T) {
	tests := []struct {
		name         string
		mtConfig     *MultitenantConfig
		dbConfig     *DatabaseConfig
		msgConfig    *MessagingConfig
		sourceConfig *SourceConfig
	}{
		{
			name: "valid_header_resolver",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},  // Empty for multitenant
			msgConfig: &MessagingConfig{}, // Empty for multitenant
		},
		{
			name: "valid_subdomain_resolver",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "subdomain",
					Domain: testDomain,
				},
				Limits: LimitsConfig{
					Tenants: 50,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "valid_composite_resolver",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:    "composite",
					Header:  testTenantHeader,
					Domain:  testDomain,
					Proxies: true,
					Order:   []string{ResolverTypeSubdomain, ResolverTypeHeader},
				},
				Limits: LimitsConfig{
					Tenants: 1000,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "valid_path_resolver",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: 2, Prefix: "/itsp"},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "valid_path_resolver_no_prefix",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: 1},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "valid_composite_resolver_with_path",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "composite",
					Header: testTenantHeader,
					Domain: testDomain,
					Path:   PathResolverConfig{Segment: 2, Prefix: "/itsp"},
					Order:  []string{ResolverTypeSubdomain, ResolverTypePath, ResolverTypeHeader},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
		{
			name: "tenants_without_messaging",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: map[string]TenantEntry{
					tenantA: {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     testTenantDBHost,
							Port:     5432,
							Database: "tenant_a",
							Username: "tenant_user",
						},
						Messaging: TenantMessagingConfig{URL: ""}, // No messaging
					},
					"tenant-b": {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     "tenant-b.db.local",
							Port:     5432,
							Database: "tenant_b",
							Username: "tenant_user",
						},
						Messaging: TenantMessagingConfig{URL: ""}, // No messaging
					},
				},
			},
			dbConfig:  &DatabaseConfig{},
			msgConfig: &MessagingConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceConfig := tt.sourceConfig
			if sourceConfig == nil {
				sourceConfig = &SourceConfig{Type: SourceTypeStatic}
			}
			err := normalizeTenantsAndCheckMultitenant(t, tt.mtConfig, tt.dbConfig, tt.msgConfig, sourceConfig)
			assert.NoError(t, err)
		})
	}
}

func TestValidateMultitenantFailures(t *testing.T) {
	tests := []struct {
		name          string
		mtConfig      *MultitenantConfig
		dbConfig      *DatabaseConfig
		msgConfig     *MessagingConfig
		sourceConfig  *SourceConfig
		expectedError string
	}{
		{
			name: "invalid_resolver_type",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: "invalid",
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.type",
		},
		{
			name: "path_resolver_missing_segment",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: 0},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.path.segment",
		},
		{
			name: "path_resolver_negative_segment",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: -1},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.path.segment",
		},
		{
			name: "path_resolver_prefix_missing_leading_slash",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: ResolverTypePath,
					Path: PathResolverConfig{Segment: 2, Prefix: "itsp"},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.path.prefix",
		},
		{
			name: "composite_with_invalid_path_segment",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "composite",
					Header: testTenantHeader,
					Domain: testDomain,
					Path:   PathResolverConfig{Segment: -2, Prefix: "/itsp"},
					Order:  []string{ResolverTypeSubdomain, ResolverTypePath, ResolverTypeHeader},
				},
				Limits:  LimitsConfig{Tenants: 100},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.resolver.path.segment",
		},
		{
			name: "invalid_limits_too_many_tenants",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: "header",
				},
				Limits: LimitsConfig{
					Tenants: 1001, // Exceeds maximum
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.limits.tenants",
		},
		{
			name: "database_configured_with_multitenant",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: "header",
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig: &DatabaseConfig{
				Host: "localhost", // This makes it configured
				Type: PostgreSQL,
			},
			msgConfig:     &MessagingConfig{},
			expectedError: "database",
		},
		{
			name: "messaging_configured_with_multitenant",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type: "header",
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: makeSampleTenants(),
			},
			dbConfig: &DatabaseConfig{},
			msgConfig: &MessagingConfig{
				Broker: BrokerConfig{
					URL: testAMQPHost, // This makes it configured
				},
			},
			expectedError: "messaging",
		},
		{
			name: "inconsistent_messaging_configuration",
			mtConfig: &MultitenantConfig{
				Enabled:  true,
				Resolver: ResolverConfig{Type: "header"},
				Limits:   LimitsConfig{Tenants: 100},
				Tenants: map[string]TenantEntry{
					tenantA: {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     testTenantDBHost,
							Port:     5432,
							Database: "tenant_a",
							Username: "tenant_user",
						},
						Messaging: TenantMessagingConfig{URL: "amqp://tenant-a"}, // Has messaging
					},
					"tenant-b": {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     "tenant-b.db.local",
							Port:     5432,
							Database: "tenant_b",
							Username: "tenant_user",
						},
						Messaging: TenantMessagingConfig{URL: ""}, // No messaging
					},
				},
			},
			dbConfig:      &DatabaseConfig{},
			msgConfig:     &MessagingConfig{},
			expectedError: "multitenant.tenants.*.messaging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceConfig := tt.sourceConfig
			if sourceConfig == nil {
				sourceConfig = &SourceConfig{Type: SourceTypeStatic}
			}
			err := normalizeTenantsAndCheckMultitenant(t, tt.mtConfig, tt.dbConfig, tt.msgConfig, sourceConfig)
			require.ErrorContains(t, err, tt.expectedError)
		})
	}
}

// TestValidateMultitenantTenantsRejectsDottedTenantID proves a tenant ID
// containing '.' is rejected: it collides with koanf's path delimiter, so the
// constructed section path multitenant.tenants.<id>.database would become
// ambiguous.
func TestValidateMultitenantTenantsRejectsDottedTenantID(t *testing.T) {
	// The dotted-ID rule lives in checkMultitenant's tenant loop, which runs
	// after the resolver/limits checks — so the resolver must be valid on its
	// own for the rejection under test to be the one that surfaces.
	mt := &MultitenantConfig{
		Enabled:  true,
		Resolver: ResolverConfig{Type: "header", Header: testTenantHeader},
		Limits:   LimitsConfig{Tenants: 100},
		Tenants: map[string]TenantEntry{
			"tenant.a": {
				Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     testTenantDBHost,
					Port:     5432,
					Database: "tenant_a",
					Username: "tenant_user",
				},
			},
		},
	}
	source := &SourceConfig{Type: SourceTypeStatic}

	err := checkMultitenant(mt, &DatabaseConfig{}, &MessagingConfig{}, source)
	assertValidationError(t, err, "cannot contain '.'")
}

func TestValidateMultitenantLimits(t *testing.T) {
	t.Run("defaults when zero", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 0}
		normalizeMultitenantLimits(&cfg)
		assert.Equal(t, 100, cfg.Tenants)
	})

	t.Run("defaults when negative", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: -1}
		normalizeMultitenantLimits(&cfg)
		assert.Equal(t, 100, cfg.Tenants)
	})

	t.Run("supports upper bound", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 1000}
		err := checkMultitenantLimits(&cfg)
		require.NoError(t, err)
		assert.Equal(t, 1000, cfg.Tenants)
	})

	t.Run("rejects exceeding upper bound", func(t *testing.T) {
		cfg := LimitsConfig{Tenants: 1001}
		err := checkMultitenantLimits(&cfg)
		require.ErrorContains(t, err, "multitenant.limits.tenants cannot exceed 1000")
	})
}

func TestValidateSourceConfig(t *testing.T) {
	tests := []struct {
		name        string
		sourceType  string
		expectError bool
	}{
		{
			name:        "valid_static",
			sourceType:  SourceTypeStatic,
			expectError: false,
		},
		{
			name:        "valid_dynamic",
			sourceType:  SourceTypeDynamic,
			expectError: false,
		},
		{
			name:        "invalid_type",
			sourceType:  "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SourceConfig{Type: tt.sourceType}
			err := validateSourceConfig(cfg)
			if tt.expectError {
				require.ErrorContains(t, err, "source.type")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateMultitenantDynamicSource(t *testing.T) {
	tests := []struct {
		name         string
		mtConfig     *MultitenantConfig
		sourceConfig *SourceConfig
		expectError  bool
		errorText    string
	}{
		{
			name: "dynamic_source_without_tenants",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				// No tenants - loaded dynamically
			},
			sourceConfig: &SourceConfig{Type: SourceTypeDynamic},
			expectError:  false,
		},
		{
			name: "dynamic_source_with_tenants",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: makeSampleTenants(), // Tenants provided but ignored
			},
			sourceConfig: &SourceConfig{Type: SourceTypeDynamic},
			expectError:  false, // Should not error, just ignored
		},
		{
			name: "static_source_empty_tenants",
			mtConfig: &MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: testTenantHeader,
				},
				Limits: LimitsConfig{
					Tenants: 100,
				},
				Tenants: map[string]TenantEntry{}, // Empty map
			},
			sourceConfig: &SourceConfig{Type: SourceTypeStatic},
			expectError:  true,
			errorText:    "empty map provided",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMultitenant(tt.mtConfig, &DatabaseConfig{}, &MessagingConfig{}, tt.sourceConfig)
			if tt.expectError {
				require.ErrorContains(t, err, tt.errorText)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestCheckMultitenantTenantEntryRejectsUnreachableIDs: a static tenant map key
// is a config section name like any other, so it obeys the same grammar the
// resolver applies to a resolved tenant ID.
func TestCheckMultitenantTenantEntryRejectsUnreachableIDs(t *testing.T) {
	tests := []struct {
		name      string
		tenantID  string
		wantField string
	}{
		{name: "underscore_in_id", tenantID: "acme_corp", wantField: "multitenant.tenants.acme_corp"},
		{name: "uppercase_in_id", tenantID: "Acme", wantField: "multitenant.tenants.Acme"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMultitenantTenantEntry(tt.tenantID, &TenantEntry{})

			assertSectionNameRejected(t, err, tt.wantField)
		})
	}
}

func TestCheckMultitenantTenantEntryAcceptsReachableIDs(t *testing.T) {
	for _, id := range []string{"acme-corp", "acme", "t1"} {
		t.Run(id, func(t *testing.T) {
			require.NoError(t, checkMultitenantTenantEntry(id, &TenantEntry{}))
		})
	}
}

// TestCheckMultitenantRejectsTenantSiblingCollision is the same shape one section over.
func TestCheckMultitenantRejectsTenantSiblingCollision(t *testing.T) {
	mt := &MultitenantConfig{
		Enabled: true,
		Tenants: map[string]TenantEntry{"acme": {}, "acme_corp": {}},
		Resolver: ResolverConfig{
			Type:   "header",
			Header: "X-Tenant-ID",
		},
	}
	source := &SourceConfig{Type: SourceTypeStatic}

	err := checkMultitenant(mt, &DatabaseConfig{}, &MessagingConfig{}, source)

	assertSectionNameRejected(t, err, "multitenant.tenants.acme_corp")
}

// TestCheckMultitenantLeavesDynamicTenantIDsToTheResolver: a dynamic source's
// tenant IDs never reach this check — they arrive at request time and the
// resolver's own grammar gates them. The static path with the same ID still fails.
func TestCheckMultitenantLeavesDynamicTenantIDsToTheResolver(t *testing.T) {
	resolver := ResolverConfig{Type: "header", Header: "X-Tenant-ID"}

	dynamic := &MultitenantConfig{
		Enabled:  true,
		Tenants:  map[string]TenantEntry{"acme_corp": {}},
		Resolver: resolver,
	}
	require.NoError(t, checkMultitenant(dynamic, &DatabaseConfig{}, &MessagingConfig{}, &SourceConfig{Type: SourceTypeDynamic}),
		"a dynamic source's tenant map is not the config's to judge")

	static := &MultitenantConfig{
		Enabled:  true,
		Tenants:  map[string]TenantEntry{"acme_corp": {}},
		Resolver: resolver,
	}
	require.Error(t, checkMultitenant(static, &DatabaseConfig{}, &MessagingConfig{}, &SourceConfig{Type: SourceTypeStatic}),
		"the same ID under a static source is still rejected")
}
