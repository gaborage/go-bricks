package credurl

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckAuthorityRefusesAnUnencodedAtAfterTheAuthority(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "question_mark_in_password", raw: "amqp://user:s3cr@zq9x?PW@broker:5672/vh"},
		{name: "slash_in_digit_password", raw: "amqp://user:1234/Spring@broker/vh"},
		{name: "hash_in_digit_password", raw: "amqp://user:1234#Spring@broker/vh"},
		{name: "slash_in_password_after_an_at", raw: "amqp://app:pw@s3cret/x@broker/vh"},
		{name: "slash_in_leading_zero_password", raw: "amqp://user:0123456/zq9x@broker/vh"},
		{name: "stream_two_segments", raw: "rabbitmq-stream://app:2024/Sp/ring@broker:5552"},
		{name: "stream_one_segment", raw: "rabbitmq-stream://app:2024/Spring@broker:5552"},
		{name: "at_in_vhost", raw: "amqp://u:p@h/my@vhost"},
		{name: "at_in_vhost_without_userinfo", raw: "amqp://h:5672/my@vhost"},
		{name: "at_in_query_value", raw: "amqp://u:p@h/vh?certfile=/a@b.pem"},
		{name: "at_in_fragment", raw: "amqp://u:p@h/vh#a@b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, CheckAuthority(tt.raw), ErrAtAfterAuthority)
		})
	}
}

func TestCheckAuthorityAcceptsAnAtInTheUserinfoOrEncoded(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "at_in_password", raw: "amqp://user:pa@ss@broker:5672/vh"},
		{name: "encoded_reserved_credentials", raw: "amqp://u:p%2Fq%3Fr%23s%40t@h:5672/%2f"},
		{name: "stream_uri", raw: "rabbitmq-stream://user:pass@broker:5552/%2f"},
		{name: "encoded_at_in_vhost", raw: "amqp://u:p@h/my%40vhost"},
		{name: "encoded_at_in_vhost_without_userinfo", raw: "amqp://h:5672/my%40vhost"},
		{name: "encoded_at_in_query_value", raw: "amqp://u:p@h/vh?certfile=/a%40b.pem"},
		{name: "encoded_at_in_fragment", raw: "amqp://u:p@h/vh#a%40b"},
		{name: "no_credentials", raw: "amqp://h:5672/vh"},
		{name: "empty", raw: ""},
		// Not a misread: url.Parse rejects it, which is another check's refusal.
		{name: "unparseable", raw: "amqp://user:s3cr?etPW@broker:5672/vh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, CheckAuthority(tt.raw))
		})
	}
}
