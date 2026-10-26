package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retiredKeysHeader is the smallest config.yaml Load accepts; each case appends to it.
const retiredKeysHeader = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"

const (
	retiredVirtualHost      = "messaging.broker.virtualhost"
	retiredRoutingExchange  = "messaging.routing.exchange"
	retiredRoutingKey       = "messaging.routing.key"
	retiredHeaders          = "messaging.headers"
	retiredVirtualHostEnv   = "MESSAGING_BROKER_VIRTUALHOST"
	retiredAllFourKeysYAML  = "messaging:\n  broker:\n    url: " + retiredKeysSecretURL + "\n    virtualhost: orders\n  routing:\n    exchange: events\n    key: default\n  headers:\n    x-app-name: svc\n"
	retiredKeysSecretURL    = "amqp://svc-user:" + retiredKeysSecretPass + "@broker.internal:5672/vh"
	retiredKeysSecretPass   = "s3cr3t-pa55"
	publisherDeclarationFix = "messaging.PublisherDeclaration"
)

// TestLoadRefusesRetiredMessagingKeys pins ADR-153: no framework code ever read these four
// keys, so every delivery of one — YAML or environment, a value, an empty string or a YAML
// null — fails startup naming the key and where the setting belongs.
func TestLoadRefusesRetiredMessagingKeys(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		env        map[string]string
		field      string
		wantAction string
	}{
		{name: "yaml_virtualhost", yaml: "messaging:\n  broker:\n    virtualhost: orders\n", field: retiredVirtualHost, wantAction: "%2F"},
		{name: "env_virtualhost", env: map[string]string{retiredVirtualHostEnv: "orders"}, field: retiredVirtualHost, wantAction: "%2F"},
		{name: "env_virtualhost_empty", env: map[string]string{retiredVirtualHostEnv: ""}, field: retiredVirtualHost, wantAction: "%2F"},
		{name: "yaml_virtualhost_null", yaml: "messaging:\n  broker:\n    virtualhost: ~\n", field: retiredVirtualHost, wantAction: "%2F"},
		// mapstructure matched the field case-insensitively, so this spelling reached it too.
		{name: "yaml_virtualhost_camel_case", yaml: "messaging:\n  broker:\n    virtualHost: orders\n", field: retiredVirtualHost, wantAction: "%2F"},
		{name: "yaml_routing_exchange", yaml: "messaging:\n  routing:\n    exchange: events\n", field: retiredRoutingExchange, wantAction: publisherDeclarationFix + ".Exchange"},
		{name: "env_routing_exchange", env: map[string]string{"MESSAGING_ROUTING_EXCHANGE": "events"}, field: retiredRoutingExchange, wantAction: publisherDeclarationFix + ".Exchange"},
		{name: "yaml_routing_exchange_null", yaml: "messaging:\n  routing:\n    exchange: ~\n", field: retiredRoutingExchange, wantAction: publisherDeclarationFix + ".Exchange"},
		{name: "yaml_routing_key", yaml: "messaging:\n  routing:\n    key: default\n", field: retiredRoutingKey, wantAction: publisherDeclarationFix + ".RoutingKey"},
		{name: "env_routing_key", env: map[string]string{"MESSAGING_ROUTING_KEY": "default"}, field: retiredRoutingKey, wantAction: publisherDeclarationFix + ".RoutingKey"},
		{name: "yaml_routing_key_null", yaml: "messaging:\n  routing:\n    key: ~\n", field: retiredRoutingKey, wantAction: publisherDeclarationFix + ".RoutingKey"},
		{name: "yaml_headers_entry", yaml: "messaging:\n  headers:\n    x-app-name: svc\n", field: retiredHeaders, wantAction: publisherDeclarationFix + ".Headers"},
		// A null at the section path records that path itself, not a key under it.
		{name: "yaml_headers_null", yaml: "messaging:\n  headers: ~\n", field: retiredHeaders, wantAction: publisherDeclarationFix + ".Headers"},
		{name: "env_headers_entry", env: map[string]string{"MESSAGING_HEADERS_SERVICE": "svc"}, field: retiredHeaders, wantAction: publisherDeclarationFix + ".Headers"},
		{name: "env_headers_scalar", env: map[string]string{"MESSAGING_HEADERS": `{"service":"svc"}`}, field: retiredHeaders, wantAction: publisherDeclarationFix + ".Headers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDeliveredEmptyFixture(t, retiredKeysHeader+tt.yaml, tt.env)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.field, cfgErr.Field)
			assert.Contains(t, cfgErr.Message, "retired")
			assert.Contains(t, cfgErr.Action, tt.wantAction)
		})
	}
}

// TestLoadRetiredVirtualHostPointsAtTheURLPath pins the one way out: the AMQP client dials
// messaging.broker.url verbatim, so the vhost is the URL path, percent-encoded.
func TestLoadRetiredVirtualHostPointsAtTheURLPath(t *testing.T) {
	_, err := loadDeliveredEmptyFixture(t, retiredKeysHeader, map[string]string{retiredVirtualHostEnv: "orders"})

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Contains(t, cfgErr.Action, "amqp://host:port/<vhost>")
	assert.Contains(t, cfgErr.Action, "%2F")
	assert.Contains(t, cfgErr.Action, retiredVirtualHostEnv)
}

// TestLoadReportsEveryRetiredKeyInOrder pins that a verbatim template copy, which delivers
// all four, is refused once with every key named, in key order.
func TestLoadReportsEveryRetiredKeyInOrder(t *testing.T) {
	_, err := loadDeliveredEmptyFixture(t, retiredKeysHeader+retiredAllFourKeysYAML, nil)

	require.Error(t, err)
	assertDeliveredEmptyError(t, err.Error(), nil, nil,
		[]string{retiredVirtualHost, retiredHeaders, retiredRoutingExchange, retiredRoutingKey})
}

// TestRetiredKeyRefusalNeverEchoesTheBrokerURL pins #2192's rule for this door: the refusal
// names keys, never the configured URL or its credentials.
func TestRetiredKeyRefusalNeverEchoesTheBrokerURL(t *testing.T) {
	_, err := loadDeliveredEmptyFixture(t, retiredKeysHeader+retiredAllFourKeysYAML, nil)

	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, retiredVirtualHost, "the refusal must be the retired-key one")
	for _, leak := range []string{retiredKeysSecretURL, retiredKeysSecretPass, "svc-user", "broker.internal"} {
		assert.NotContains(t, msg, leak)
	}
}

// TestLoadWithoutRetiredKeysUnchanged pins the other half: a config that names none of the
// keys, its vhost in the URL path, loads as it did.
func TestLoadWithoutRetiredKeysUnchanged(t *testing.T) {
	const url = "amqp://localhost:5672/orders"
	cfg, err := loadDeliveredEmptyFixture(t, retiredKeysHeader+"messaging:\n  broker:\n    url: "+url+"\n", nil)

	require.NoError(t, err)
	assert.Equal(t, url, cfg.Messaging.Broker.URL)
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
