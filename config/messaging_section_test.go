package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyMessagingDefaults(t *testing.T) {
	tests := []struct {
		name                      string
		config                    MessagingConfig
		expectedReconnectDelay    time.Duration
		expectedReinitDelay       time.Duration
		expectedResendDelay       time.Duration
		expectedConnectionTimeout time.Duration
		expectedMaxDelay          time.Duration
		expectedMaxPublishers     int
		expectedPublisherIdleTTL  time.Duration
	}{
		{
			name: "zero_values_apply_all_defaults",
			config: MessagingConfig{
				Broker: BrokerConfig{URL: testAMQPHost},
			},
			expectedReconnectDelay:    defaultReconnectDelay,
			expectedReinitDelay:       defaultReinitDelay,
			expectedResendDelay:       defaultResendDelay,
			expectedConnectionTimeout: defaultConnectionTimeout,
			expectedMaxDelay:          defaultMaxReconnectDelay,
			expectedMaxPublishers:     defaultMaxPublishers,
			expectedPublisherIdleTTL:  defaultPublisherIdleTTL,
		},
		{
			name: "explicit_values_preserved",
			config: MessagingConfig{
				Broker: BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{
					Delay:             10 * time.Second,
					ReinitDelay:       5 * time.Second,
					ResendDelay:       8 * time.Second,
					ConnectionTimeout: 45 * time.Second,
					MaxDelay:          120 * time.Second,
				},
				Publisher: PublisherPoolConfig{
					MaxCached: 100,
					IdleTTL:   5 * time.Minute,
				},
			},
			expectedReconnectDelay:    10 * time.Second,
			expectedReinitDelay:       5 * time.Second,
			expectedResendDelay:       8 * time.Second,
			expectedConnectionTimeout: 45 * time.Second,
			expectedMaxDelay:          120 * time.Second,
			expectedMaxPublishers:     100,
			expectedPublisherIdleTTL:  5 * time.Minute,
		},
		{
			name: "partial_config_applies_missing_defaults",
			config: MessagingConfig{
				Broker: BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{
					Delay: 15 * time.Second, // Only delay set
				},
			},
			expectedReconnectDelay:    15 * time.Second,   // Preserved
			expectedReinitDelay:       defaultReinitDelay, // Defaulted
			expectedResendDelay:       defaultResendDelay, // Defaulted
			expectedConnectionTimeout: defaultConnectionTimeout,
			expectedMaxDelay:          defaultMaxReconnectDelay,
			expectedMaxPublishers:     defaultMaxPublishers,
			expectedPublisherIdleTTL:  defaultPublisherIdleTTL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Single-tenant mode: these cases exercise the non-IdleTTL defaults and the
			// single-tenant IdleTTL default; see TestApplyMessagingDefaultsIdleTTLModeAware
			// for the mode-dependent IdleTTL behavior.
			err := normalizeMessaging(&tt.config, false)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedReconnectDelay, tt.config.Reconnect.Delay, "Reconnect.Delay mismatch")
			assert.Equal(t, tt.expectedReinitDelay, tt.config.Reconnect.ReinitDelay, "Reconnect.ReinitDelay mismatch")
			assert.Equal(t, tt.expectedResendDelay, tt.config.Reconnect.ResendDelay, "Reconnect.ResendDelay mismatch")
			assert.Equal(t, tt.expectedConnectionTimeout, tt.config.Reconnect.ConnectionTimeout, "Reconnect.ConnectionTimeout mismatch")
			assert.Equal(t, tt.expectedMaxDelay, tt.config.Reconnect.MaxDelay, "Reconnect.MaxDelay mismatch")
			assert.Equal(t, tt.expectedMaxPublishers, tt.config.Publisher.MaxCached, "Publisher.MaxCached mismatch")
			assert.Equal(t, tt.expectedPublisherIdleTTL, tt.config.Publisher.IdleTTL, "Publisher.IdleTTL mismatch")
		})
	}
}

// TestApplyMessagingDefaultsIdleTTLModeAware proves Publisher.IdleTTL defaulting is
// deployment-mode-dependent: 1h single-tenant, 10m multi-tenant. Before this fix,
// applyMessagingDefaults always applied the single-tenant 1h default regardless of
// mode, silently raising the multi-tenant effective TTL and making
// ManagerConfigBuilder.BuildMessagingOptions' multi-tenant fallback dead code (see
// app/managers_test.go).
func TestApplyMessagingDefaultsIdleTTLModeAware(t *testing.T) {
	t.Run("single_tenant_unset_defaults_to_1h", func(t *testing.T) {
		cfg := &MessagingConfig{Broker: BrokerConfig{URL: testAMQPHost}}
		require.NoError(t, normalizeMessaging(cfg, false))
		assert.Equal(t, defaultPublisherIdleTTL, cfg.Publisher.IdleTTL)
		assert.Equal(t, 1*time.Hour, cfg.Publisher.IdleTTL)
	})

	t.Run("multi_tenant_unset_defaults_to_10m", func(t *testing.T) {
		cfg := &MessagingConfig{Broker: BrokerConfig{URL: testAMQPHost}}
		require.NoError(t, normalizeMessaging(cfg, true))
		assert.Equal(t, defaultPublisherIdleTTLMultiTenant, cfg.Publisher.IdleTTL)
		assert.Equal(t, 10*time.Minute, cfg.Publisher.IdleTTL)
	})

	t.Run("explicit_value_preserved_regardless_of_mode", func(t *testing.T) {
		cfg := &MessagingConfig{
			Broker:    BrokerConfig{URL: testAMQPHost},
			Publisher: PublisherPoolConfig{IdleTTL: 42 * time.Minute},
		}
		require.NoError(t, normalizeMessaging(cfg, true))
		assert.Equal(t, 42*time.Minute, cfg.Publisher.IdleTTL)
	})
}

// TestValidateMessagingPublisherIdleTTLModeAwareEndToEnd proves the mode-aware
// default reaches Publisher.IdleTTL through the full Validate(cfg) entry point
// (not just the internal normalizeMessaging seam), matching the real config.Load()
// path that runs before app/bootstrap.go builds the manager options.
func TestValidateMessagingPublisherIdleTTLModeAwareEndToEnd(t *testing.T) {
	t.Run("single_tenant_validate_yields_1h", func(t *testing.T) {
		cfg := &Config{
			App:       createValidAppConfig(),
			Server:    createValidServerConfig(),
			Log:       createValidLogConfig(),
			Messaging: MessagingConfig{Broker: BrokerConfig{URL: testAMQPHost}},
		}
		require.NoError(t, Validate(cfg))
		assert.Equal(t, 1*time.Hour, cfg.Messaging.Publisher.IdleTTL)
	})

	t.Run("multi_tenant_validate_yields_10m", func(t *testing.T) {
		cfg := &Config{
			App:    createValidAppConfig(),
			Server: createValidServerConfig(),
			Log:    createValidLogConfig(),
			Multitenant: MultitenantConfig{
				Enabled:  true,
				Resolver: ResolverConfig{Type: ResolverTypeHeader, Header: testTenantHeader},
			},
			// Dynamic source: no static multitenant.tenants, so root-level messaging is
			// still permitted alongside multitenant.enabled (validateNoSingleTenantConflict
			// only fires for a static source with tenants configured).
			Messaging: MessagingConfig{Broker: BrokerConfig{URL: testAMQPHost}},
			Source:    SourceConfig{Type: SourceTypeDynamic},
		}
		require.NoError(t, Validate(cfg))
		assert.Equal(t, 10*time.Minute, cfg.Messaging.Publisher.IdleTTL)
	})
}

func TestApplyMessagingDefaultsMaxPublishAttempts(t *testing.T) {
	t.Run("zero_gets_default", func(t *testing.T) {
		cfg := &MessagingConfig{Broker: BrokerConfig{URL: testAMQPHost}}
		require.NoError(t, normalizeMessaging(cfg, false))
		assert.Equal(t, defaultMaxPublishAttempts, cfg.Reconnect.MaxPublishAttempts)
	})
	t.Run("explicit_value_preserved", func(t *testing.T) {
		cfg := &MessagingConfig{
			Broker:    BrokerConfig{URL: testAMQPHost},
			Reconnect: ReconnectConfig{MaxPublishAttempts: 9},
		}
		require.NoError(t, normalizeMessaging(cfg, false))
		assert.Equal(t, 9, cfg.Reconnect.MaxPublishAttempts)
	})
}

func TestApplyMessagingDefaultsReadyTimeout(t *testing.T) {
	t.Run("zero_gets_default", func(t *testing.T) {
		cfg := &MessagingConfig{Broker: BrokerConfig{URL: testAMQPHost}}
		require.NoError(t, normalizeMessaging(cfg, false))
		assert.Equal(t, defaultReadyTimeout, cfg.Reconnect.ReadyTimeout)
	})
	t.Run("explicit_value_preserved", func(t *testing.T) {
		cfg := &MessagingConfig{
			Broker:    BrokerConfig{URL: testAMQPHost},
			Reconnect: ReconnectConfig{ReadyTimeout: 9 * time.Second},
		}
		require.NoError(t, normalizeMessaging(cfg, false))
		assert.Equal(t, 9*time.Second, cfg.Reconnect.ReadyTimeout)
	})
}

func TestApplyMessagingDefaultsCleanupInterval(t *testing.T) {
	t.Run("zero_gets_default", func(t *testing.T) {
		cfg := &MessagingConfig{Broker: BrokerConfig{URL: testAMQPHost}}
		require.NoError(t, normalizeMessaging(cfg, false))
		assert.Equal(t, DefaultPublisherCleanupInterval, cfg.Publisher.CleanupInterval)
	})
	t.Run("explicit_value_preserved", func(t *testing.T) {
		cfg := &MessagingConfig{
			Broker:    BrokerConfig{URL: testAMQPHost},
			Publisher: PublisherPoolConfig{CleanupInterval: 90 * time.Second},
		}
		require.NoError(t, normalizeMessaging(cfg, false))
		assert.Equal(t, 90*time.Second, cfg.Publisher.CleanupInterval)
	})
}

func TestApplyMessagingDefaultsNegativeValues(t *testing.T) {
	tests := []struct {
		name          string
		config        MessagingConfig
		errorContains string
	}{
		{
			name: "negative_reconnect_delay_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{Delay: -1 * time.Second},
			},
			errorContains: "messaging.reconnect.delay",
		},
		{
			name: "negative_max_publishers_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Publisher: PublisherPoolConfig{MaxCached: -1},
			},
			errorContains: "messaging.publisher.maxcached",
		},
		{
			name: "negative_reinit_delay_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{ReinitDelay: -1 * time.Second},
			},
			errorContains: "messaging.reconnect.reinitdelay",
		},
		{
			name: "negative_resend_delay_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{ResendDelay: -1 * time.Second},
			},
			errorContains: "messaging.reconnect.resenddelay",
		},
		{
			name: "negative_connection_timeout_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{ConnectionTimeout: -1 * time.Second},
			},
			errorContains: "messaging.reconnect.connectiontimeout",
		},
		{
			name: "negative_max_delay_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{MaxDelay: -1 * time.Second},
			},
			errorContains: "messaging.reconnect.maxdelay",
		},
		{
			name: "negative_max_publish_attempts_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{MaxPublishAttempts: -1},
			},
			errorContains: "messaging.reconnect.maxpublishattempts",
		},
		{
			name: "negative_ready_timeout_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Reconnect: ReconnectConfig{ReadyTimeout: -1 * time.Second},
			},
			errorContains: "messaging.reconnect.readytimeout",
		},
		{
			name: "negative_publisher_idle_ttl_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Publisher: PublisherPoolConfig{IdleTTL: -1 * time.Second},
			},
			errorContains: "messaging.publisher.idlettl",
		},
		{
			name: "negative_publisher_cleanup_interval_rejected",
			config: MessagingConfig{
				Broker:    BrokerConfig{URL: testAMQPHost},
				Publisher: PublisherPoolConfig{CleanupInterval: -1 * time.Second},
			},
			errorContains: "messaging.publisher.cleanupinterval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeMessaging(&tt.config, false)
			assertValidationError(t, err, tt.errorContains)
		})
	}
}

// normalizeAndCheckMessaging runs both halves of the messaging split in phase
// order, for tables whose cases need a fill before the rejection they pin.
func normalizeAndCheckMessaging(cfg *MessagingConfig, multitenant bool) error {
	if err := normalizeMessaging(cfg, multitenant); err != nil {
		return err
	}
	return checkMessaging(cfg, multitenant)
}

func TestIsMessagingConfigured(t *testing.T) {
	tests := []struct {
		name     string
		config   MessagingConfig
		expected bool
	}{
		{
			name:     "empty_config_not_configured",
			config:   MessagingConfig{},
			expected: false,
		},
		{
			name: "broker_url_configured",
			config: MessagingConfig{
				Broker: BrokerConfig{
					URL: testAMQPHost,
				},
			},
			expected: true,
		},
		{
			name: "broker_url_with_virtualhost",
			config: MessagingConfig{
				Broker: BrokerConfig{
					URL:         testAMQPHost,
					VirtualHost: "/test",
				},
			},
			expected: true,
		},
		{
			name: "empty_broker_url_not_configured",
			config: MessagingConfig{
				Broker: BrokerConfig{
					URL: "",
				},
			},
			expected: false,
		},
		{
			name: "whitespace_broker_url_is_configured",
			config: MessagingConfig{
				Broker: BrokerConfig{
					URL: "   ",
				},
			},
			expected: true, // Whitespace is still considered configured
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsMessagingConfigured(&tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestValidateMessagingAppliesDefaultsWhenBrokerURLEmpty pins the #659 fix: defaults
// and negative-value rejection apply even when the root broker URL is empty (see
// normalizeMessaging's doc comment for why). Field-by-field defaulting is covered by
// TestApplyMessagingDefaults; these subtests pin the no-gate behavior and the
// mode-aware Publisher defaults.
func TestValidateMessagingAppliesDefaultsWhenBrokerURLEmpty(t *testing.T) {
	t.Run("single_tenant_empty_broker_applies_defaults", func(t *testing.T) {
		cfg := &MessagingConfig{} // empty Broker.URL
		require.NoError(t, normalizeMessaging(cfg, false))

		assert.Equal(t, defaultConnectionTimeout, cfg.Reconnect.ConnectionTimeout)
		assert.Equal(t, defaultPublisherIdleTTL, cfg.Publisher.IdleTTL)
		assert.Equal(t, defaultMaxPublishers, cfg.Publisher.MaxCached)
	})

	t.Run("multi_tenant_empty_broker_applies_mode_aware_publisher_defaults", func(t *testing.T) {
		cfg := &MessagingConfig{}
		require.NoError(t, normalizeMessaging(cfg, true))

		assert.Equal(t, defaultConnectionTimeout, cfg.Reconnect.ConnectionTimeout)
		assert.Equal(t, defaultPublisherIdleTTLMultiTenant, cfg.Publisher.IdleTTL)
		// MaxCached stays zero in multi-tenant mode so BuildMessagingOptions scales
		// the publisher pool to the tenant limit (app/managers.go).
		assert.Zero(t, cfg.Publisher.MaxCached)
	})

	t.Run("empty_broker_negative_value_rejected", func(t *testing.T) {
		cfg := &MessagingConfig{Reconnect: ReconnectConfig{Delay: -1}}
		require.Error(t, normalizeMessaging(cfg, false))
	})

	t.Run("multi_tenant_negative_maxcached_rejected", func(t *testing.T) {
		cfg := &MessagingConfig{Publisher: PublisherPoolConfig{MaxCached: -1}}
		require.Error(t, normalizeMessaging(cfg, true))
	})
}

// TestValidateMessagingRejectsMaxDelayBelowDelay pins the cross-field guard: an
// inverted pair would be silently clamped by computeBackoff, leaving the
// configured ceiling ignored (#662).
func TestValidateMessagingRejectsMaxDelayBelowDelay(t *testing.T) {
	t.Run("explicit_maxdelay_below_default_delay_rejected", func(t *testing.T) {
		cfg := &MessagingConfig{Reconnect: ReconnectConfig{MaxDelay: 2 * time.Second}} // delay defaults to 5s
		err := normalizeAndCheckMessaging(cfg, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "messaging.reconnect.maxdelay")
	})

	t.Run("explicit_delay_above_default_maxdelay_rejected", func(t *testing.T) {
		cfg := &MessagingConfig{Reconnect: ReconnectConfig{Delay: 90 * time.Second}} // maxdelay defaults to 60s
		require.Error(t, normalizeAndCheckMessaging(cfg, false))
	})

	t.Run("consistent_pair_accepted", func(t *testing.T) {
		cfg := &MessagingConfig{Reconnect: ReconnectConfig{Delay: 10 * time.Second, MaxDelay: 2 * time.Minute}}
		require.NoError(t, normalizeAndCheckMessaging(cfg, false))
	})

	// The bound is <, not <=: an equal pair is a fixed, non-growing backoff,
	// which computeBackoff honors exactly as written.
	t.Run("maxdelay_equal_to_delay_accepted", func(t *testing.T) {
		cfg := &MessagingConfig{Reconnect: ReconnectConfig{Delay: 5 * time.Second, MaxDelay: 5 * time.Second}}
		require.NoError(t, normalizeAndCheckMessaging(cfg, false))
	})

	t.Run("defaults_accepted", func(t *testing.T) {
		require.NoError(t, normalizeAndCheckMessaging(&MessagingConfig{}, false))
	})

	// The mode flag only selects Publisher.IdleTTL/MaxCached defaults; the
	// maxdelay >= delay rule itself is mode-independent.
	t.Run("multi_tenant_maxdelay_below_delay_rejected", func(t *testing.T) {
		cfg := &MessagingConfig{Reconnect: ReconnectConfig{MaxDelay: 2 * time.Second}}
		err := normalizeAndCheckMessaging(cfg, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "messaging.reconnect.maxdelay")
	})
}

// TestValidateMultiTenantStaticAppliesMessagingDefaultsEndToEnd proves a multi-tenant
// static config — where the root broker URL is necessarily empty — yields effective
// messaging defaults through the full Validate() entry point, so the outbox
// publishtimeout Fail-Fast guards have real values to compare against (see
// normalizeMessaging's doc comment).
func TestValidateMultiTenantStaticAppliesMessagingDefaultsEndToEnd(t *testing.T) {
	cfg := &Config{
		App:    createValidAppConfig(),
		Server: createValidServerConfig(),
		Log:    createValidLogConfig(),
		Multitenant: MultitenantConfig{
			Enabled: true,
			Resolver: ResolverConfig{
				Type:   ResolverTypeHeader,
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
					Messaging: TenantMessagingConfig{URL: testAMQPHost},
				},
			},
		},
		Source: SourceConfig{Type: SourceTypeStatic},
		// No root broker URL: validateNoSingleTenantConflict rejects one in
		// static multi-tenant mode (other root messaging.* keys stay legal).
	}

	require.NoError(t, Validate(cfg))

	assert.Equal(t, 30*time.Second, cfg.Messaging.Reconnect.ConnectionTimeout)
	assert.Equal(t, 10*time.Minute, cfg.Messaging.Publisher.IdleTTL)
	// Zero preserved: BuildMessagingOptions scales the pool to the tenant limit.
	assert.Zero(t, cfg.Messaging.Publisher.MaxCached)
}

// streamsFixturePassword is a fixture value, not a credential — the leak assertions
// below prove it never reaches an error string.
const streamsFixturePassword = "fixture-pw"

func TestValidateMessagingStreamsURIScheme(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		wantErr string
	}{
		{name: "unset_uri_is_valid", uri: ""},
		{name: "plain_scheme_accepted", uri: "rabbitmq-stream://svc:" + streamsFixturePassword + "@broker:5552/%2f"},
		{name: "tls_scheme_accepted", uri: "rabbitmq-stream+tls://svc:" + streamsFixturePassword + "@broker:5551/%2f"},
		{
			name:    "amqp_scheme_rejected",
			uri:     "amqp://svc:" + streamsFixturePassword + "@broker:5672/",
			wantErr: "'amqp' is not supported must be one of: rabbitmq-stream://, rabbitmq-stream+tls://",
		},
		{
			name:    "schemeless_value_rejected",
			uri:     "broker:5552",
			wantErr: "is not supported",
		},
		{
			name:    "unparseable_uri_rejected",
			uri:     "rabbitmq-stream://svc:" + streamsFixturePassword + "@broker:55 52/",
			wantErr: "messaging.streams.uri must be a valid URI",
		},
		{
			// The realistic typo: a missing "//" parses as an opaque URI whose scheme
			// still passes the allowlist, leaving nothing to dial.
			name:    "missing_double_slash_rejected",
			uri:     "rabbitmq-stream:broker:5552",
			wantErr: "messaging.streams.uri must include a host",
		},
		{
			name:    "host_less_uri_with_credentials_rejected",
			uri:     "rabbitmq-stream:svc:" + streamsFixturePassword + "@/vhost",
			wantErr: "messaging.streams.uri must include a host",
		},
		{
			// url.URL.Host carries the port, so ":5552" is a non-empty Host with an
			// empty Hostname: a Host == "" check passes it through with nothing to dial.
			name:    "port_without_hostname_rejected",
			uri:     "rabbitmq-stream://:5552/%2f",
			wantErr: "messaging.streams.uri must include a host",
		},
		{
			name:    "port_without_hostname_with_credentials_rejected",
			uri:     "rabbitmq-stream://svc:" + streamsFixturePassword + "@:5552/%2f",
			wantErr: "messaging.streams.uri must include a host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &MessagingConfig{Streams: StreamsConfig{URI: tt.uri}}

			err := normalizeAndCheckMessaging(cfg, false)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotContains(t, err.Error(), streamsFixturePassword,
				"messaging.streams.uri carries credentials; they must never reach an error message")
		})
	}
}

func TestValidateMessagingStreamsRejectsMultiTenant(t *testing.T) {
	cfg := &MessagingConfig{Streams: StreamsConfig{
		URI: "rabbitmq-stream://svc:" + streamsFixturePassword + "@broker:5552/%2f",
	}}

	err := normalizeAndCheckMessaging(cfg, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging.streams single-tenant only")
	assert.Contains(t, err.Error(), "multi-tenant stream consumption is not yet supported")
	assert.NotContains(t, err.Error(), streamsFixturePassword)
}

func TestValidateMessagingStreamsAllowsMultiTenantWithoutURI(t *testing.T) {
	cfg := &MessagingConfig{}

	require.NoError(t, normalizeAndCheckMessaging(cfg, true),
		"multi-tenant deployments that declare no streams stay valid")
}

func TestValidateMessagingStreamsAddressResolver(t *testing.T) {
	tests := []struct {
		name     string
		resolver StreamsAddressResolverConfig
		wantErr  string
	}{
		{name: "both_unset", resolver: StreamsAddressResolverConfig{}},
		{name: "both_set", resolver: StreamsAddressResolverConfig{Host: "lb.example.com", Port: 5552}},
		// Both ends of the accepted range are inclusive; without these the range
		// check could be off by one at either edge and every other case still pass.
		{name: "lowest_valid_port", resolver: StreamsAddressResolverConfig{Host: "lb.example.com", Port: 1}},
		{name: "highest_valid_port", resolver: StreamsAddressResolverConfig{Host: "lb.example.com", Port: 65535}},
		{
			name:     "port_without_host",
			resolver: StreamsAddressResolverConfig{Port: 5552},
			wantErr:  "messaging.streams.addressresolver.host must be set",
		},
		{
			name:     "host_without_port",
			resolver: StreamsAddressResolverConfig{Host: "lb.example.com"},
			wantErr:  "messaging.streams.addressresolver.port invalid value: 0 must be one of: 1-65535",
		},
		{
			name:     "port_above_range",
			resolver: StreamsAddressResolverConfig{Host: "lb.example.com", Port: 65536},
			wantErr:  "messaging.streams.addressresolver.port invalid value: 65536 must be one of: 1-65535",
		},
		{
			name:     "negative_port",
			resolver: StreamsAddressResolverConfig{Host: "lb.example.com", Port: -1},
			wantErr:  "messaging.streams.addressresolver.port invalid value: -1 must be one of: 1-65535",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &MessagingConfig{Streams: StreamsConfig{AddressResolver: tt.resolver}}

			err := normalizeAndCheckMessaging(cfg, false)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestApplyStreamsDefaults(t *testing.T) {
	t.Run("zero_applies_defaults", func(t *testing.T) {
		cfg := &MessagingConfig{}

		require.NoError(t, normalizeMessaging(cfg, false))

		assert.Equal(t, 500, cfg.Streams.OffsetStore.CountBeforeStorage)
		assert.Equal(t, defaultStreamsOffsetCount, cfg.Streams.OffsetStore.CountBeforeStorage)
		assert.Equal(t, 5*time.Second, cfg.Streams.OffsetStore.FlushInterval)
		assert.Equal(t, defaultStreamsOffsetInterval, cfg.Streams.OffsetStore.FlushInterval)
	})

	t.Run("explicit_values_preserved", func(t *testing.T) {
		cfg := &MessagingConfig{Streams: StreamsConfig{OffsetStore: StreamsOffsetStoreConfig{
			CountBeforeStorage: 25,
			FlushInterval:      750 * time.Millisecond,
		}}}

		require.NoError(t, normalizeMessaging(cfg, false))

		assert.Equal(t, 25, cfg.Streams.OffsetStore.CountBeforeStorage)
		assert.Equal(t, 750*time.Millisecond, cfg.Streams.OffsetStore.FlushInterval)
	})

	t.Run("negative_count_rejected", func(t *testing.T) {
		cfg := &MessagingConfig{Streams: StreamsConfig{OffsetStore: StreamsOffsetStoreConfig{CountBeforeStorage: -1}}}

		err := normalizeMessaging(cfg, false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "messaging.streams.offsetstore.countbeforestorage must be non-negative")
	})

	t.Run("negative_interval_rejected", func(t *testing.T) {
		cfg := &MessagingConfig{Streams: StreamsConfig{OffsetStore: StreamsOffsetStoreConfig{FlushInterval: -time.Second}}}

		err := normalizeMessaging(cfg, false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "messaging.streams.offsetstore.flushinterval must be non-negative")
	})
}

// TestValidateStreamsRejectsMultiTenantEndToEnd proves the fail-fast reaches the
// public Validate(cfg) entry point, not just the internal seam.
func TestValidateStreamsRejectsMultiTenantEndToEnd(t *testing.T) {
	cfg := &Config{
		App:    createValidAppConfig(),
		Server: createValidServerConfig(),
		Log:    createValidLogConfig(),
		Messaging: MessagingConfig{Streams: StreamsConfig{
			URI: "rabbitmq-stream://svc:" + streamsFixturePassword + "@broker:5552/%2f",
		}},
		Multitenant: MultitenantConfig{
			Enabled:  true,
			Resolver: ResolverConfig{Type: "header", Header: testTenantHeader},
			Tenants: map[string]TenantEntry{
				"acme": {Database: DatabaseConfig{
					Type:     PostgreSQL,
					Host:     "acme.db",
					Port:     5432,
					Database: "acme",
					Username: "acme_user",
				}},
			},
		},
		Source: SourceConfig{Type: SourceTypeStatic},
	}

	err := Validate(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "single-tenant only")
}

// TestValidateMessagingTenancy pins the messaging kind's tenancy: unset normalizes
// to per-tenant, both accepted values pass in either deployment mode (shared is a
// no-op single-tenant, ADR-041 env-parity), and anything else fails check naming
// both accepted values.
func TestValidateMessagingTenancy(t *testing.T) {
	tests := []struct {
		name        string
		tenancy     string
		multitenant bool
		wantErr     bool
	}{
		{name: "unset_defaults_to_per_tenant", tenancy: "", multitenant: false},
		{name: "per_tenant_accepted", tenancy: TenancyPerTenant, multitenant: true},
		{name: "shared_accepted", tenancy: TenancyShared, multitenant: true},
		{name: "shared_accepted_single_tenant", tenancy: TenancyShared, multitenant: false},
		{name: "unknown_rejected", tenancy: "Shared", multitenant: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				App:       createValidAppConfig(),
				Server:    createValidServerConfig(),
				Log:       createValidLogConfig(),
				Messaging: MessagingConfig{Tenancy: tt.tenancy},
			}
			if tt.multitenant {
				cfg.Multitenant = MultitenantConfig{
					Enabled:  true,
					Resolver: ResolverConfig{Type: "header", Header: testTenantHeader},
					Tenants: map[string]TenantEntry{
						"acme": {Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     "acme.db",
							Port:     5432,
							Database: "acme",
							Username: "acme_user",
						}},
					},
				}
				cfg.Source = SourceConfig{Type: SourceTypeStatic}
			}

			err := Validate(cfg)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "messaging.tenancy")
				assert.Contains(t, err.Error(), TenancyPerTenant)
				assert.Contains(t, err.Error(), TenancyShared)
				return
			}
			require.NoError(t, err)

			want := tt.tenancy
			if want == "" {
				want = TenancyPerTenant
			}
			assert.Equal(t, want, cfg.Messaging.Tenancy)
		})
	}
}

// TestValidateSharedMessagingTenancyCrossSectionRules pins the two cross-section
// rules the messaging tenancy moves: under shared, a per-tenant messaging block is
// unreachable and fails check, while the root messaging block becomes legal beside
// static tenants because it IS the control-plane broker.
func TestValidateSharedMessagingTenancyCrossSectionRules(t *testing.T) {
	tests := []struct {
		name       string
		tenantURL  string
		rootBroker string
		tenancy    string
		wantErrs   []string
	}{
		{
			name:       "per_tenant_brokers_ok",
			tenantURL:  "amqp://guest:" + streamsFixturePassword + "@tenant-broker:5672/",
			rootBroker: "",
			tenancy:    TenancyPerTenant,
		},
		{
			name:       "shared_with_tenant_broker_rejected",
			tenantURL:  "amqp://guest:" + streamsFixturePassword + "@tenant-broker:5672/",
			rootBroker: "amqp://guest:" + streamsFixturePassword + "@control-plane:5672/",
			tenancy:    TenancyShared,
			wantErrs:   []string{"multitenant.tenants.*.messaging", TenancyShared},
		},
		{
			name:       "shared_root_broker_ok",
			tenantURL:  "",
			rootBroker: "amqp://guest:" + streamsFixturePassword + "@control-plane:5672/",
			tenancy:    TenancyShared,
		},
		{
			name:       "per_tenant_root_broker_rejected",
			tenantURL:  "",
			rootBroker: "amqp://guest:" + streamsFixturePassword + "@control-plane:5672/",
			tenancy:    TenancyPerTenant,
			wantErrs:   []string{"messaging", "not allowed when static tenants are configured"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newSharedTenancyConfig(tt.rootBroker, tt.tenancy)
			for id := range cfg.Multitenant.Tenants {
				tenant := cfg.Multitenant.Tenants[id]
				tenant.Messaging = TenantMessagingConfig{URL: tt.tenantURL}
				cfg.Multitenant.Tenants[id] = tenant
			}

			err := Validate(cfg)

			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantErrs {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestValidateMessagingStreamsUnderSharedTenancy pins the streams gate moving from
// "multi-tenant" to "per-tenant tenancy": one Environment per tenant still does not
// exist, but shared tenancy consumes streams once on the control-plane key.
func TestValidateMessagingStreamsUnderSharedTenancy(t *testing.T) {
	const streamsURI = "rabbitmq-stream://svc:" + streamsFixturePassword + "@broker:5552/%2f"

	t.Run("streams_rejected_per_tenant", func(t *testing.T) {
		cfg := newSharedTenancyConfig("", TenancyPerTenant)
		cfg.Messaging.Streams = StreamsConfig{URI: streamsURI}

		err := Validate(cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "single-tenant only")
		assert.NotContains(t, err.Error(), streamsFixturePassword)
	})

	t.Run("streams_accepted_shared", func(t *testing.T) {
		cfg := newSharedTenancyConfig("", TenancyShared)
		cfg.Messaging.Streams = StreamsConfig{URI: streamsURI}

		require.NoError(t, Validate(cfg))
	})
}

// newSharedTenancyConfig builds a multi-tenant static-source config with one
// tenant carrying a database, the given root broker URL and the given messaging
// tenancy.
func newSharedTenancyConfig(rootBroker, tenancy string) *Config {
	tenantDB := func(name string) DatabaseConfig {
		return DatabaseConfig{
			Type:     PostgreSQL,
			Host:     name + ".db",
			Port:     5432,
			Database: name,
			Username: name + "_user",
		}
	}
	return &Config{
		App:    createValidAppConfig(),
		Server: createValidServerConfig(),
		Log:    createValidLogConfig(),
		Messaging: MessagingConfig{
			Broker:  BrokerConfig{URL: rootBroker},
			Tenancy: tenancy,
		},
		Multitenant: MultitenantConfig{
			Enabled:  true,
			Resolver: ResolverConfig{Type: "header", Header: testTenantHeader},
			Tenants: map[string]TenantEntry{
				"acme": {Database: tenantDB("acme")},
			},
		},
		Source: SourceConfig{Type: SourceTypeStatic},
	}
}

// TestCheckMessagingSeal covers the selector shape rules: key reachability
// and dots, value grammar, and sorted first-error reporting.
func TestCheckMessagingSeal(t *testing.T) {
	tests := []struct {
		name      string
		active    map[string]string
		wantField string
		wantMsg   string
	}{
		{name: "absent", active: nil},
		{name: "valid", active: map[string]string{"svc-payments-sign": "v2", "svc-enc": "v10"}},
		{name: "empty_key", active: map[string]string{"": "v1"}, wantField: "messaging.seal.active", wantMsg: "cannot be empty or contain '.'"},
		{name: "dotted_key", active: map[string]string{"svc.sign": "v1"}, wantField: "messaging.seal.active", wantMsg: "cannot be empty or contain '.'"},
		{name: "unreachable_key", active: map[string]string{"Svc_Sign": "v1"}, wantField: "messaging.seal.active.Svc_Sign", wantMsg: "not reachable by an environment variable"},
		{name: "value_missing_v", active: map[string]string{"svc-sign": "2"}, wantField: "messaging.seal.active.svc-sign", wantMsg: `generation "2" must be v<N>`},
		{name: "value_zero", active: map[string]string{"svc-sign": "v0"}, wantField: "messaging.seal.active.svc-sign", wantMsg: `generation "v0" must be v<N>`},
		{name: "value_leading_zero", active: map[string]string{"svc-sign": "v01"}, wantField: "messaging.seal.active.svc-sign", wantMsg: `generation "v01" must be v<N>`},
		{name: "value_with_hyphen", active: map[string]string{"svc-sign": "-v1"}, wantField: "messaging.seal.active.svc-sign", wantMsg: `generation "-v1" must be v<N>`},
		{name: "first_bad_key_in_sorted_order", active: map[string]string{"zz": "v0", "aa": "v01"}, wantField: "messaging.seal.active.aa", wantMsg: `"v01"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMessagingSeal(&SealConfig{Active: tt.active})
			if tt.wantField == "" {
				assert.NoError(t, err)
				return
			}
			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			assert.Contains(t, cfgErr.Message, tt.wantMsg)
		})
	}
}

// TestCheckMessagingRunsSealRule pins that checkMessaging reaches the seal
// rule, so config.Validate carries it.
func TestCheckMessagingRunsSealRule(t *testing.T) {
	cfg := MessagingConfig{Tenancy: TenancyPerTenant, Seal: SealConfig{Active: map[string]string{"svc-sign": "v01"}}}
	err := checkMessaging(&cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging.seal.active.svc-sign")
}

// TestValidateMessagingPublishTimeout pins messaging.publishtimeout's three
// states — unset (unbounded), negative (rejected), and set-below-the-floor
// (rejected) — against NORMALIZED reconnect operands. The floor is
// readytimeout + connectiontimeout: a shorter bound could truncate a healthy
// cold-path first attempt (readiness wait + one confirmation) into a false
// failure, the same hazard outbox.validatePublishTimeout refuses startup over.
func TestValidateMessagingPublishTimeout(t *testing.T) {
	// The floor at defaults, derived rather than spelled: a change to either
	// default must move this test's boundary with it, not silently desync from it.
	const defaultFloor = defaultReadyTimeout + defaultConnectionTimeout

	tests := []struct {
		name               string
		config             MessagingConfig
		errorContains      []string
		wantPublishTimeout time.Duration
	}{
		{
			// The opt-in default: absent key stays 0 after normalization, so no
			// deployment acquires a bound it never configured.
			name:               "unset_stays_zero_and_is_accepted",
			config:             MessagingConfig{},
			wantPublishTimeout: 0,
		},
		{
			name:          "negative_rejected",
			config:        MessagingConfig{PublishTimeout: -time.Second},
			errorContains: []string{"messaging.publishtimeout"},
		},
		{
			// Exactly at the floor is the smallest legal bound: the check is <,
			// not <=. Without this case a `<=` mutant survives.
			name:               "exactly_at_floor_accepted",
			config:             MessagingConfig{PublishTimeout: defaultFloor},
			wantPublishTimeout: defaultFloor,
		},
		{
			// One nanosecond under the floor is the tightest rejection: it pins
			// the comparison AND its arithmetic, since any wrong operand pairing
			// moves the boundary by whole seconds.
			name:   "floor_minus_one_nanosecond_rejected",
			config: MessagingConfig{PublishTimeout: defaultFloor - time.Nanosecond},
			errorContains: []string{
				"messaging.publishtimeout",
				"messaging.reconnect.readytimeout",
				"messaging.reconnect.connectiontimeout",
			},
		},
		{
			// The floor reads NORMALIZED values: readytimeout is unset here, so
			// its 5s default must participate. Against the explicit 20s
			// connectiontimeout the floor is 25s, and 24s is under it — a check
			// that compared pre-normalization values would see readytimeout 0,
			// compute a 20s floor, and wrongly accept.
			name: "floor_uses_normalized_readytimeout_default",
			config: MessagingConfig{
				PublishTimeout: 24 * time.Second,
				Reconnect:      ReconnectConfig{ConnectionTimeout: 20 * time.Second},
			},
			errorContains: []string{"messaging.publishtimeout"},
		},
		{
			// Same normalized floor of 25s, one second above it: the pair proves
			// the rejection above is the boundary and not a blanket refusal.
			name: "above_normalized_floor_accepted",
			config: MessagingConfig{
				PublishTimeout: 26 * time.Second,
				Reconnect:      ReconnectConfig{ConnectionTimeout: 20 * time.Second},
			},
			wantPublishTimeout: 26 * time.Second,
		},
		{
			// Custom operands on both sides: floor 10s+40s = 50s, so 49s is
			// rejected. This is the case a swapped or dropped operand fails.
			name: "floor_sums_both_explicit_operands",
			config: MessagingConfig{
				PublishTimeout: 49 * time.Second,
				Reconnect:      ReconnectConfig{ReadyTimeout: 10 * time.Second, ConnectionTimeout: 40 * time.Second},
			},
			errorContains: []string{"messaging.publishtimeout"},
		},
		{
			// time.ParseDuration accepts either operand alone, but their int64
			// nanosecond sum wraps negative — and a negative floor would let EVERY
			// positive bound through, silently disabling the check. Rejected as an
			// overflow rather than compared against the wrapped value.
			name: "floor_operands_overflowing_duration_rejected",
			config: MessagingConfig{
				PublishTimeout: time.Second,
				Reconnect: ReconnectConfig{
					ReadyTimeout:      2000000 * time.Hour,
					ConnectionTimeout: 2000000 * time.Hour,
				},
			},
			errorContains: []string{"messaging.publishtimeout", "overflows time.Duration"},
		},
		{
			// The same operands one notch below the wrap still sum cleanly, so the
			// guard rejects an overflow and not merely a large configuration.
			name: "large_but_non_overflowing_floor_accepted",
			config: MessagingConfig{
				PublishTimeout: 2000000 * time.Hour,
				Reconnect: ReconnectConfig{
					ReadyTimeout:      1000 * time.Hour,
					ConnectionTimeout: 1000 * time.Hour,
				},
			},
			wantPublishTimeout: 2000000 * time.Hour,
		},
		{
			name: "at_custom_floor_accepted",
			config: MessagingConfig{
				PublishTimeout: 50 * time.Second,
				Reconnect:      ReconnectConfig{ReadyTimeout: 10 * time.Second, ConnectionTimeout: 40 * time.Second},
			},
			wantPublishTimeout: 50 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			err := normalizeAndCheckMessaging(&cfg, false)

			if len(tt.errorContains) == 0 {
				require.NoError(t, err)
				assert.Equal(t, tt.wantPublishTimeout, cfg.PublishTimeout,
					"normalization must leave messaging.publishtimeout exactly as configured")
				return
			}
			require.Error(t, err)
			for _, want := range tt.errorContains {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestValidateMessagingPublishTimeoutFloorIsModeIndependent guards the rule
// against the deployment-mode flag: multitenant only selects Publisher.IdleTTL
// and MaxCached defaults, so the floor must reject and accept identically.
func TestValidateMessagingPublishTimeoutFloorIsModeIndependent(t *testing.T) {
	rejected := MessagingConfig{PublishTimeout: 34 * time.Second} // floor at defaults is 35s
	err := normalizeAndCheckMessaging(&rejected, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging.publishtimeout")

	accepted := MessagingConfig{PublishTimeout: 35 * time.Second}
	require.NoError(t, normalizeAndCheckMessaging(&accepted, true))
}
