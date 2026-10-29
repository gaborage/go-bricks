package messaging

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// TestCreateAMQPClientLogsBrokerURLInOnePosture pins the broker_url field to credurl.RedactForLog.
func TestCreateAMQPClientLogsBrokerURLInOnePosture(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		secrets []string
	}{
		{name: "standard_amqp_url_with_credentials", input: "amqp://guest:secret123@localhost:5672/", want: "amqp://guest:xxxxx@localhost:5672/", secrets: []string{"secret123"}},
		{name: "amqps_secure_url", input: "amqps://admin:SuperSecret@rabbitmq.prod:5671/production", want: "amqps://admin:xxxxx@rabbitmq.prod:5671/production", secrets: []string{"SuperSecret"}},
		{name: "url_with_special_chars_in_password", input: "amqp://user:p@ss%20w0rd!@localhost:5672/vhost", want: "amqp://user:xxxxx@localhost:5672/vhost", secrets: []string{"p@ss", "w0rd"}},
		{name: "url_with_complex_vhost", input: "amqp://guest:guest@localhost:5672/%2Fproduction", want: "amqp://guest:xxxxx@localhost:5672/%2Fproduction", secrets: []string{":guest@"}},
		{name: "url_without_credentials", input: "amqp://localhost:5672/", want: "amqp://localhost:5672/"},
		{name: "amqps_without_credentials", input: "amqps://rabbitmq.prod:5671/", want: "amqps://rabbitmq.prod:5671/"},
		{name: "url_with_username_only_no_password", input: "amqp://guest@localhost:5672/", want: "amqp://xxxxx@localhost:5672/", secrets: []string{"guest"}},
		{name: "url_with_query_parameters", input: "amqp://user:pass@localhost:5672/?heartbeat=30&connection_timeout=10", want: "amqp://user:xxxxx@localhost:5672/?xxxxx", secrets: []string{"pass", "heartbeat"}},
		{name: "query_string_is_masked_not_leaked", input: "amqp://user:secret@host:5672/vhost?token=leakme&heartbeat=60", want: "amqp://user:xxxxx@host:5672/vhost?xxxxx", secrets: []string{"secret", "leakme"}},
		{name: "malformed_url_masked_whole", input: "not-a-valid-url", want: "xxxxx", secrets: []string{"valid"}},
		{name: "rejected_without_at_masked_whole", input: "amqp://user:pass/broker", want: "xxxxx", secrets: []string{"pass"}},
		{name: "forgot_host_path_masked_whole", input: "amqp://user:QZ/XJK", want: "xxxxx", secrets: []string{"QZ", "XJK"}},
		{name: "forgot_host_at_in_username_masked_whole", input: "amqp://u@ser:QZXJ/vh", want: "xxxxx", secrets: []string{"QZXJ"}},
		{name: "forgot_host_at_in_password_masked_whole", input: "amqp://user:pa@ss:QZXJ", want: "xxxxx", secrets: []string{"pa", "ss", "QZXJ"}},
		{name: "empty_url", input: "", want: "xxxxx"},
		{name: "non_amqp_scheme", input: "http://user:pass@localhost:80/", want: "http://user:xxxxx@localhost:80/", secrets: []string{"pass"}},
		{name: "empty_host_masked_whole", input: "amqp://guest:secret@/vhost", want: "xxxxx", secrets: []string{"secret", "guest"}},
		{name: "encoded_credentials_are_masked", input: "amqp://user%2Fname:p%40ss@localhost:5672/vh%2Fost", want: "amqp://user%2Fname:xxxxx@localhost:5672/vh%2Fost", secrets: []string{"p%40ss", "p@ss"}},
		{name: "url_with_ip_address", input: "amqp://admin:secret@192.168.1.100:5672/", want: "amqp://admin:xxxxx@192.168.1.100:5672/", secrets: []string{"secret"}},
		{name: "url_with_long_password", input: "amqp://user:verylongpasswordwithmanychars123456789@localhost:5672/", want: "amqp://user:xxxxx@localhost:5672/", secrets: []string{"verylongpassword"}},
		{name: "keeps_debugging_info", input: "amqp://admin:secret@rabbitmq.example.com:5672/production", want: "amqp://admin:xxxxx@rabbitmq.example.com:5672/production", secrets: []string{"secret"}},
		// url.Parse reads "user:1234" as host:port, so a renderer that trusts it shows the password.
		{name: "misparsed_password_masked", input: "amqp://user:1234/Spring@broker/vh", want: "amqp://user:xxxxx@broker/vh", secrets: []string{"1234", "Spring"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := newRecordingLogger()
			manager := NewMessagingManager(
				&stubMessagingSource{urls: map[string]string{testTenantID: tt.input}},
				log, ManagerOptions{}, func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} })
			defer func() { _ = manager.Close() }()

			_, err := manager.createAMQPClient(context.Background(), testTenantID)

			require.NoError(t, err)
			got := log.Line(t, "Created AMQP client for key").Values("broker_url")
			require.Equal(t, []string{tt.want}, got)
			for _, secret := range tt.secrets {
				assert.NotContains(t, got[0], secret)
			}
		})
	}
}
