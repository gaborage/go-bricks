package outbox

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

func TestEventIDFromHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers amqp.Table
		wantID  string
		wantOK  bool
	}{
		{"string", amqp.Table{HeaderEventID: "evt-1"}, "evt-1", true},
		{"bytes", amqp.Table{HeaderEventID: []byte("evt-2")}, "evt-2", true},
		{"absent", amqp.Table{}, "", false},
		{"nil_table", nil, "", false},
		{"empty_string", amqp.Table{HeaderEventID: ""}, "", false},
		{"empty_bytes", amqp.Table{HeaderEventID: []byte{}}, "", false},
		{"wrong_type", amqp.Table{HeaderEventID: 42}, "", false},
		{"other_header_only", amqp.Table{HeaderEventType: "order.created"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := EventIDFromHeaders(tc.headers)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantID, id)
		})
	}
}

func TestHeaderConstants(t *testing.T) {
	// The relay is the single source of truth; these literals must not drift.
	assert.Equal(t, "x-outbox-event-id", HeaderEventID)
	assert.Equal(t, "x-outbox-event-type", HeaderEventType)
}
