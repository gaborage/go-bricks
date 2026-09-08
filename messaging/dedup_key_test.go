package messaging

import (
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateEventIDVariesTheGrammar pins both boundaries of the grammar
// ^[A-Za-z0-9_-]{1,128}$: every accepted class, the 128-byte ceiling, and one
// rejection per way out of it — including the sealed-key shape family:jti.
func TestValidateEventIDVariesTheGrammar(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"uuid", "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", true},
		{"every_class", "aZ09_-", true},
		{"single_byte", "x", true},
		{"max_length_128", strings.Repeat("a", 128), true},
		{"empty", "", false},
		{"length_129", strings.Repeat("a", 129), false},
		{"colon_sealed_shape", "rsa:9f0c2b1e", false},
		{"newline", "evt-1\n", false},
		{"space", "evt 1", false},
		{"non_ascii", "evt-é", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEventID(tc.id)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidEventID)
			if tc.id != "" {
				assert.NotContains(t, err.Error(), tc.id, "the error names the length, never the id")
			}
		})
	}
}

// TestValidateEventIDErrorCarriesLengthOnly pins the disclosure rule on the
// over-long path, where the value is the most likely to be attacker-shaped.
func TestValidateEventIDErrorCarriesLengthOnly(t *testing.T) {
	err := ValidateEventID(strings.Repeat("s", 129))
	require.ErrorIs(t, err, ErrInvalidEventID)
	assert.Contains(t, err.Error(), "129 bytes")
	assert.NotContains(t, err.Error(), "sss")
}

func TestMetadataDedupKey(t *testing.T) {
	cases := []struct {
		name    string
		headers amqp.Table
		want    string
		wantErr bool
	}{
		{"string_header", amqp.Table{HeaderEventID: "evt-1"}, "evt-1", false},
		{"bytes_header", amqp.Table{HeaderEventID: []byte("evt-2")}, "evt-2", false},
		{"absent", amqp.Table{}, "", true},
		{"nil_table", nil, "", true},
		{"empty_string", amqp.Table{HeaderEventID: ""}, "", true},
		{"wrong_type", amqp.Table{HeaderEventID: int32(7)}, "", true},
		{"malformed_colon", amqp.Table{HeaderEventID: "hmac:abc"}, "", true},
		{"malformed_bytes", amqp.Table{HeaderEventID: []byte("a b")}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := Metadata{delivery: &amqp.Delivery{Headers: tc.headers}}
			got, err := meta.DedupKey()
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidEventID)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestMetadataDedupKeyZeroValue pins the inert zero Metadata: no delivery is an
// absent header, not a panic.
func TestMetadataDedupKeyZeroValue(t *testing.T) {
	_, err := Metadata{}.DedupKey()
	assert.ErrorIs(t, err, ErrInvalidEventID)
}

// TestMetadataSealedIsFalseForPlainConsumers pins that the answer is per type:
// a publisher-written header cannot flip it.
func TestMetadataSealedIsFalseForPlainConsumers(t *testing.T) {
	for name, meta := range map[string]Metadata{
		"zero":            {},
		"plain_delivery":  {delivery: &amqp.Delivery{Headers: amqp.Table{HeaderEventID: "evt-1"}}},
		"sealed_looking":  {delivery: &amqp.Delivery{Headers: amqp.Table{"x-sealed": true, "jti": "abc"}}},
		"encrypted_ctype": {delivery: &amqp.Delivery{ContentType: "application/jose"}},
	} {
		t.Run(name, func(t *testing.T) {
			env, ok := meta.Sealed()
			assert.False(t, ok)
			assert.Equal(t, SealedEnvelope{}, env)
		})
	}
}

func TestMetadataSealedAndDedupKeyForASealedDelivery(t *testing.T) {
	env := SealedEnvelope{JTI: "jti-1", SignKid: "svc-sign-v2", SignFamily: "svc-sign", EncKid: "aud-enc-v1", EventType: "evt", TenantID: "acme"}
	meta := Metadata{delivery: &amqp.Delivery{Headers: amqp.Table{HeaderEventID: "header-id"}}, sealed: &env}

	got, ok := meta.Sealed()
	assert.True(t, ok)
	assert.Equal(t, env, got)

	key, err := meta.DedupKey()
	require.NoError(t, err)
	assert.Equal(t, "svc-sign:jti-1", key, "the sealed key wins over any header the publisher wrote")
	assert.True(t, IsSealedDedupKey(key))
	assert.ErrorIs(t, ValidateEventID(key), ErrInvalidEventID, "a sealed key is outside the header grammar by construction")
}

// TestMetadataDedupKeyPrefersTheStampOverTheMessageIDProperty pins the
// precedence half of #1547: on a go-bricks producer the stamp is
// framework-written and the message_id property is caller-written, so a present
// stamp wins whatever the property says — and a present-but-malformed stamp
// errors rather than falling through to the property.
func TestMetadataDedupKeyPrefersTheStampOverTheMessageIDProperty(t *testing.T) {
	cases := []struct {
		name      string
		headers   amqp.Table
		messageID string
		want      string
		wantErr   bool
	}{
		{"stamp_and_property", amqp.Table{HeaderEventID: "evt-1"}, "prop-1", "evt-1", false},
		{"stamp_without_property", amqp.Table{HeaderEventID: "evt-1"}, "", "evt-1", false},
		{"stamp_with_malformed_property", amqp.Table{HeaderEventID: "evt-1"}, "a b", "evt-1", false},
		{"malformed_stamp_with_valid_property", amqp.Table{HeaderEventID: "a b"}, "prop-1", "", true},
		{"empty_stamp_with_valid_property", amqp.Table{HeaderEventID: ""}, "prop-1", "", true},
		{"wrong_type_stamp_with_valid_property", amqp.Table{HeaderEventID: int32(7)}, "prop-1", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := Metadata{delivery: &amqp.Delivery{Headers: tc.headers, MessageId: tc.messageID}}
			got, err := meta.DedupKey()
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidEventID)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestMetadataDedupKeyFallsBackToTheMessageIDProperty pins the fallback half of
// #1547: a producer that follows the standard but is not go-bricks writes
// message_id and no stamp, and that delivery must still be processable through
// inbox.ProcessOnce. The property answers to the SAME grammar as the stamp, so
// `:` stays excluded and a property can never mint a sealed key — ADR-097's
// suppression-attack closure holds.
func TestMetadataDedupKeyFallsBackToTheMessageIDProperty(t *testing.T) {
	cases := []struct {
		name      string
		headers   amqp.Table
		messageID string
		want      string
		wantErr   bool
	}{
		{"valid_property", amqp.Table{}, "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", false},
		{"valid_property_nil_table", nil, "prop-1", "prop-1", false},
		{"every_grammar_class", amqp.Table{}, "aZ09_-", "aZ09_-", false},
		{"max_length_128", amqp.Table{}, strings.Repeat("a", 128), strings.Repeat("a", 128), false},
		{"absent_property", amqp.Table{}, "", "", true},
		{"length_129", amqp.Table{}, strings.Repeat("a", 129), "", true},
		{"malformed_space", amqp.Table{}, "prop 1", "", true},
		{"malformed_colon_sealed_shape", amqp.Table{}, "svc-payments-sign:9f0c2b1e", "", true},
		{"other_headers_do_not_stamp", amqp.Table{"x-idempotency-key": "business-key"}, "prop-1", "prop-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := Metadata{delivery: &amqp.Delivery{Headers: tc.headers, MessageId: tc.messageID}}
			got, err := meta.DedupKey()
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidEventID)
				assert.Empty(t, got)
				if tc.messageID != "" {
					assert.NotContains(t, err.Error(), tc.messageID, "the error names the length, never the id")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestMetadataDedupKeyIgnoresTheMessageIDPropertyWhenSealed pins that the
// sealed branch is untouched by #1547: the key is composed from the verified
// envelope whatever the wire carries.
func TestMetadataDedupKeyIgnoresTheMessageIDPropertyWhenSealed(t *testing.T) {
	env := SealedEnvelope{JTI: "jti-1", SignFamily: "svc-sign"}
	for name, delivery := range map[string]*amqp.Delivery{
		"property_only":      {MessageId: "prop-1"},
		"property_and_stamp": {Headers: amqp.Table{HeaderEventID: "evt-1"}, MessageId: "prop-1"},
		"malformed_property": {MessageId: "a b"},
	} {
		t.Run(name, func(t *testing.T) {
			key, err := Metadata{delivery: delivery, sealed: &env}.DedupKey()
			require.NoError(t, err)
			assert.Equal(t, "svc-sign:jti-1", key)
		})
	}
}

// TestMetadataMessageID pins the accessor the fallback reads through, including
// the inert zero value.
func TestMetadataMessageID(t *testing.T) {
	assert.Equal(t, "prop-1", Metadata{delivery: &amqp.Delivery{MessageId: "prop-1"}}.MessageID())
	assert.Empty(t, Metadata{delivery: &amqp.Delivery{}}.MessageID())
	assert.Empty(t, Metadata{}.MessageID())
}
