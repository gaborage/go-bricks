package amqpurl

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/credurl"
)

func TestCheckRefusesWhatTheClientCannotDial(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{name: "heartbeat_out_of_range", raw: "amqp://app:pw@broker:5672/vh?heartbeat=70000", want: ErrQueryOutOfRange},
		{name: "heartbeat_not_a_number", raw: "amqp://app:pw@broker:5672/vh?heartbeat=soon", want: ErrQueryOutOfRange},
		{name: "channel_max_out_of_range", raw: "amqp://app:pw@broker:5672/vh?channel_max=70000", want: ErrQueryOutOfRange},
		{name: "unencoded_percent_in_password", raw: "amqp://app:p%w@broker:5672/vh", want: ErrNotAMQPURL},
		{name: "space_inside", raw: "amqp://app:pw@broker:5672/v h", want: ErrNotAMQPURL},
		{name: "prose", raw: "not a url at all", want: ErrNotAMQPURL},
		{name: "non_numeric_port", raw: "amqp://app:pw@broker:port/vh", want: ErrNotAMQPURL},
		{name: "http_scheme", raw: "http://broker/", want: ErrUnsupportedScheme},
		{name: "stream_scheme", raw: "rabbitmq-stream://app:pw@broker:5552/vh", want: ErrUnsupportedScheme},
		{name: "schemeless_host_port", raw: "broker:5672", want: ErrUnsupportedScheme},
		{name: "empty", raw: "", want: ErrUnsupportedScheme},
		{name: "port_above_range", raw: "amqp://app:pw@broker:65536/vh", want: ErrPortOutOfRange},
		{name: "port_zero", raw: "amqp://app:pw@broker:0/vh", want: ErrPortOutOfRange},
		{name: "port_beyond_int32", raw: "amqp://app:pw@broker:99999999999/vh", want: ErrPortOutOfRange},
		{name: "port_beyond_int64", raw: "amqp://app:pw@broker:99999999999999999999/vh", want: ErrPortOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, Check(tt.raw), tt.want)
		})
	}
}

func TestCheckAcceptsWhatTheClientParses(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "full_url", raw: "amqp://app:pw@broker:5672/vh"},
		{name: "tls_scheme", raw: "amqps://app:pw@broker:5671/vh"},
		{name: "no_port", raw: "amqp://app:pw@broker/vh"},
		{name: "lowest_port", raw: "amqp://broker:1/vh"},
		{name: "highest_port", raw: "amqp://broker:65535/vh"},
		{name: "leading_zero_port", raw: "amqp://h:05672/vh"},
		{name: "two_segment_path", raw: "amqp://u:p@h/a/b"},
		{name: "encoded_credentials", raw: "amqp://u:p%2Fq%3Fr%23s%40t@h:5672/%2f"},
		{name: "heartbeat_in_range", raw: "amqp://u:p@h/vh?heartbeat=65535"},
		{name: "empty_username", raw: "amqp://:pw@h/vh"},
		{name: "host_less", raw: "amqp://u:p@/vh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, Check(tt.raw))
		})
	}
}

// The misread is judged first: these also trip a port or a scheme class, which would
// name a symptom of the misread, not its cause.
func TestCheckRefusesAMisreadURLBeforeItsOtherDefects(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "slash_in_digit_password", raw: "amqp://user:1234/Spring@broker/vh"},
		{name: "port_out_of_range_too", raw: "amqp://user:0123456/zq9x@broker/vh"},
		{name: "unsupported_scheme_too", raw: "http://user:1234/Spring@broker/vh"},
		{name: "query_out_of_range_too", raw: "amqp://u:p@h/vh?heartbeat=70000&certfile=/a@b.pem"},
		{name: "at_in_vhost", raw: "amqp://u:p@h/my@vhost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Check(tt.raw)

			require.ErrorIs(t, err, credurl.ErrAtAfterAuthority)
			assert.Equal(t, credurl.AuthorityHint, HintFor(err))
		})
	}
}

func TestHintForAMalformedURLIsThePercentEncodingHint(t *testing.T) {
	for _, err := range []error{ErrNotAMQPURL, ErrUnsupportedScheme, ErrPortOutOfRange, ErrQueryOutOfRange} {
		assert.Equal(t, hint, HintFor(err))
	}
}
