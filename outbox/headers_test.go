package outbox

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/internal/publishdoor"
	"github.com/gaborage/go-bricks/messaging"
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
	// Namespaced so no caller header can collide with the framework's own stamp, and
	// spelled here because the writer (Publish) and the stripper (Plan) must agree.
	assert.Equal(t, "x-gobricks-content-type", headerContentTypeStamp)
}

func TestTakeFrameworkStampsRemovesBothAndReportsThem(t *testing.T) {
	tests := map[string]struct {
		headers         map[string]any
		wantStamp       string
		wantContentType string
	}{
		"both_stamps":      {headers: map[string]any{messaging.TenantStampHeader: "acme", headerContentTypeStamp: publishdoor.ContentTypeJSON, "keep": 1}, wantStamp: "acme", wantContentType: publishdoor.ContentTypeJSON},
		"tenant_only":      {headers: map[string]any{messaging.TenantStampHeader: "acme"}, wantStamp: "acme"},
		"content_only":     {headers: map[string]any{headerContentTypeStamp: publishdoor.ContentTypeJSON}, wantContentType: publishdoor.ContentTypeJSON},
		"neither":          {headers: map[string]any{"keep": 1}},
		"nil_map":          {headers: nil},
		"non_string_stamp": {headers: map[string]any{messaging.TenantStampHeader: 7, headerContentTypeStamp: []byte("x")}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			stamp, contentType := takeFrameworkStamps(tt.headers)

			assert.Equal(t, tt.wantStamp, stamp)
			assert.Equal(t, tt.wantContentType, contentType)
			assert.NotContains(t, tt.headers, messaging.TenantStampHeader, "a stamp left behind reaches the wire")
			assert.NotContains(t, tt.headers, headerContentTypeStamp)
		})
	}
}
