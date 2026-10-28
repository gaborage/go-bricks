package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retiredKeysHeader is the smallest config.yaml Load accepts; each case appends to it.
const retiredKeysHeader = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"

const (
	retiredVirtualHost     = "messaging.broker.virtualhost"
	retiredRoutingExchange = "messaging.routing.exchange"
	retiredRoutingKey      = "messaging.routing.key"
	retiredHeaders         = "messaging.headers"
	retiredVirtualHostEnv  = "MESSAGING_BROKER_VIRTUALHOST"
)

// TestLoadRefusesRetiredMessagingKeys pins ADR-153: no framework code ever read these four
// keys, so every delivery of one — YAML or environment, a value, an empty string or a YAML
// null — fails startup naming the key and where the setting belongs.
func TestLoadRefusesRetiredMessagingKeys(t *testing.T) {
	wantAction := map[string][]string{
		retiredVirtualHost:     {retiredVirtualHostEnv, "amqps://host:port/<vhost>, with each / in the name encoded as %2F"},
		retiredRoutingExchange: {"MESSAGING_ROUTING_EXCHANGE", "messaging.PublisherOptions.Exchange"},
		retiredRoutingKey:      {"MESSAGING_ROUTING_KEY", "messaging.PublisherOptions.RoutingKey"},
		retiredHeaders:         {"MESSAGING_HEADERS", "messaging.PublisherOptions.Headers"},
	}
	tests := []struct {
		name  string
		yaml  string
		env   map[string]string
		field string
	}{
		{name: "yaml_virtualhost", yaml: "messaging:\n  broker:\n    virtualhost: orders\n", field: retiredVirtualHost},
		{name: "env_virtualhost", env: map[string]string{retiredVirtualHostEnv: "orders"}, field: retiredVirtualHost},
		{name: "env_virtualhost_empty", env: map[string]string{retiredVirtualHostEnv: ""}, field: retiredVirtualHost},
		{name: "yaml_virtualhost_null", yaml: "messaging:\n  broker:\n    virtualhost: ~\n", field: retiredVirtualHost},
		// mapstructure matched the field case-insensitively, so this spelling reached it too.
		{name: "yaml_virtualhost_camel_case", yaml: "messaging:\n  broker:\n    virtualHost: orders\n", field: retiredVirtualHost},
		// U+017F for s: mapstructure's EqualFold matches it, so the lookup must too.
		{name: "yaml_virtualhost_long_s", yaml: "messaging:\n  broker:\n    virtualhoſt: orders\n", field: retiredVirtualHost},
		{name: "yaml_headers_long_s_section", yaml: "meſſaging:\n  headers:\n    x-app-name: svc\n", field: retiredHeaders},
		{name: "yaml_routing_exchange", yaml: "messaging:\n  routing:\n    exchange: events\n", field: retiredRoutingExchange},
		{name: "env_routing_exchange", env: map[string]string{"MESSAGING_ROUTING_EXCHANGE": "events"}, field: retiredRoutingExchange},
		{name: "yaml_routing_exchange_null", yaml: "messaging:\n  routing:\n    exchange: ~\n", field: retiredRoutingExchange},
		{name: "yaml_routing_key", yaml: "messaging:\n  routing:\n    key: default\n", field: retiredRoutingKey},
		{name: "env_routing_key", env: map[string]string{"MESSAGING_ROUTING_KEY": "default"}, field: retiredRoutingKey},
		{name: "yaml_routing_key_null", yaml: "messaging:\n  routing:\n    key: ~\n", field: retiredRoutingKey},
		{name: "yaml_headers_entry", yaml: "messaging:\n  headers:\n    x-app-name: svc\n", field: retiredHeaders},
		// A null at the section path records that path itself, not a key under it.
		{name: "yaml_headers_null", yaml: "messaging:\n  headers: ~\n", field: retiredHeaders},
		{name: "env_headers_entry", env: map[string]string{"MESSAGING_HEADERS_SERVICE": "svc"}, field: retiredHeaders},
		{name: "env_headers_scalar", env: map[string]string{"MESSAGING_HEADERS": `{"service":"svc"}`}, field: retiredHeaders},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDeliveredEmptyFixture(t, retiredKeysHeader+tt.yaml, tt.env)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.field, cfgErr.Field)
			assert.Contains(t, cfgErr.Message, "retired")
			for _, want := range wantAction[tt.field] {
				assert.Contains(t, cfgErr.Action, want)
			}
		})
	}
}

// TestLoadReportsEveryRetiredKeyWithoutTheBrokerURL pins that a verbatim template copy,
// which delivers all four, is refused once with every key named in key order, and that the
// refusal never echoes the configured URL or its credentials (#2192).
func TestLoadReportsEveryRetiredKeyWithoutTheBrokerURL(t *testing.T) {
	const secret = "s3cr3t-pa55"
	yaml := "messaging:\n  broker:\n    url: amqps://svc-user:" + secret + "@broker.internal:5671/vh\n" +
		"    virtualhost: orders\n  routing:\n    exchange: events\n    key: default\n  headers:\n    x-app-name: svc\n"

	_, err := loadDeliveredEmptyFixture(t, retiredKeysHeader+yaml, nil)

	require.Error(t, err)
	assertDeliveredEmptyError(t, err.Error(), nil, []string{secret, "svc-user", "broker.internal"},
		[]string{retiredVirtualHost, retiredHeaders, retiredRoutingExchange, retiredRoutingKey})
}

// TestLoadWithoutRetiredKeysUnchanged pins the other half: a config that delivers none of the
// keys loads as it did. An empty headers map delivers no key and configures nothing, so it is
// accepted, as ADR-078 accepts an empty sequence (ADR-153).
func TestLoadWithoutRetiredKeysUnchanged(t *testing.T) {
	const url = testAMQPHost + "orders"
	tests := []struct {
		name string
		yaml string
	}{
		{name: "vhost_in_url_path", yaml: "messaging:\n  broker:\n    url: " + url + "\n"},
		{name: "empty_headers_map_configures_nothing", yaml: "messaging:\n  broker:\n    url: " + url + "\n  headers: {}\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadDeliveredEmptyFixture(t, retiredKeysHeader+tt.yaml, nil)

			require.NoError(t, err)
			assert.Equal(t, url, cfg.Messaging.Broker.URL)
		})
	}
}

// TestLoadFromMapLeavesRetiredKeysToValidate pins where the refusal lives: LoadFromMap skips
// Validate by design, so the migrate CLI's --source-config stays silent, while every
// construction path that validates refuses the key.
func TestLoadFromMapLeavesRetiredKeysToValidate(t *testing.T) {
	cfg, err := LoadFromMap(map[string]any{retiredVirtualHost: "orders"})
	require.NoError(t, err)

	var cfgErr *ConfigError
	require.ErrorAs(t, Validate(cfg), &cfgErr)
	assert.Equal(t, retiredVirtualHost, cfgErr.Field)
}

// TestLoadRetiredKeyEvictedByAnOverlayIsNotRefused pins that delivery means the final tree
// (ADR-104): a base layer's retired key that an overlay's null evicted leaves nothing to
// refuse.
func TestLoadRetiredKeyEvictedByAnOverlayIsNotRefused(t *testing.T) {
	_, err := loadConfigFixture(t, map[string]string{
		testConfigFileYAML: retiredKeysHeader + "messaging:\n  broker:\n    virtualhost: orders\n",
		"config.prod.yaml": "messaging:\n  broker:\n",
	}, map[string]string{"APP_ENV": "prod"})

	require.NoError(t, err)
}
