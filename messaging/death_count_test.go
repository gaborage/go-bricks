package messaging

import (
	"math"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

const (
	deathTestQueue      = "orders.work"
	deathTestOtherQueue = "orders.retry"
)

func deathHeaders(entries ...any) amqp.Table {
	return amqp.Table{headerXDeath: entries}
}

func deathEntry(queue, reason, count any) amqp.Table {
	return amqp.Table{"queue": queue, "reason": reason, "count": count, "exchange": ""}
}

func TestDeathCountMatchesQueueAndReason(t *testing.T) {
	tests := []struct {
		name       string
		headers    amqp.Table
		emptyQueue bool
		wantCount  int64
		wantOK     bool
	}{
		{name: "nil_headers", headers: nil},
		{name: "no_x_death_header", headers: amqp.Table{"other": "value"}},
		{name: "header_not_an_array", headers: amqp.Table{headerXDeath: "rejected"}},
		{name: "header_is_a_table", headers: amqp.Table{headerXDeath: deathEntry(deathTestQueue, DeathReasonRejected, int64(3))}},
		{name: "entry_not_a_table", headers: deathHeaders("not-a-table", int64(3))},
		{
			name:    "no_matching_queue",
			headers: deathHeaders(deathEntry(deathTestOtherQueue, DeathReasonRejected, int64(3))),
		},
		{
			name:    "matching_queue_different_reason",
			headers: deathHeaders(deathEntry(deathTestQueue, DeathReasonExpired, int64(3))),
		},
		{
			name:      "amqp_table_entry_matches",
			headers:   deathHeaders(deathEntry(deathTestQueue, DeathReasonRejected, int64(3))),
			wantCount: 3, wantOK: true,
		},
		{
			name: "plain_map_entry_matches",
			headers: deathHeaders(map[string]any{
				"queue": deathTestQueue, "reason": DeathReasonRejected, "count": int64(4),
			}),
			wantCount: 4, wantOK: true,
		},
		{
			name: "skips_non_matching_entries",
			headers: deathHeaders(
				"junk",
				deathEntry(deathTestOtherQueue, DeathReasonExpired, int64(7)),
				deathEntry(deathTestQueue, DeathReasonExpired, int64(8)),
				deathEntry(deathTestQueue, DeathReasonRejected, int64(2)),
			),
			wantCount: 2, wantOK: true,
		},
		{
			name: "first_match_decides_even_when_unusable",
			headers: deathHeaders(
				deathEntry(deathTestQueue, DeathReasonRejected, "5"),
				deathEntry(deathTestQueue, DeathReasonRejected, int64(9)),
			),
		},
		{
			name:      "queue_and_reason_as_bytes",
			headers:   deathHeaders(deathEntry([]byte(deathTestQueue), []byte(DeathReasonRejected), int64(5))),
			wantCount: 5, wantOK: true,
		},
		{
			name:      "queue_as_string_reason_as_bytes",
			headers:   deathHeaders(deathEntry(deathTestQueue, []byte(DeathReasonRejected), int64(6))),
			wantCount: 6, wantOK: true,
		},
		{
			name:      "queue_as_bytes_reason_as_string",
			headers:   deathHeaders(deathEntry([]byte(deathTestQueue), DeathReasonRejected, int64(7))),
			wantCount: 7, wantOK: true,
		},
		{
			name:       "missing_queue_does_not_match_empty_queue",
			headers:    deathHeaders(amqp.Table{"reason": DeathReasonRejected, "count": int64(1)}),
			emptyQueue: true,
		},
		{
			name:    "wrong_typed_queue",
			headers: deathHeaders(deathEntry(int64(1), DeathReasonRejected, int64(1))),
		},
		{
			name:    "wrong_typed_reason",
			headers: deathHeaders(deathEntry(deathTestQueue, true, int64(1))),
		},
		{
			name:    "missing_count",
			headers: deathHeaders(amqp.Table{"queue": deathTestQueue, "reason": DeathReasonRejected}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			queue := deathTestQueue
			if tc.emptyQueue {
				queue = ""
			}
			count, ok := DeathCount(tc.headers, queue, DeathReasonRejected)
			assert.Equal(t, tc.wantCount, count)
			assert.Equal(t, tc.wantOK, ok)
		})
	}
}

func TestDeathCountAcceptsEveryIntegerKind(t *testing.T) {
	tests := []struct {
		name      string
		count     any
		wantCount int64
		wantOK    bool
	}{
		{name: "int", count: int(3), wantCount: 3, wantOK: true},
		{name: "int8", count: int8(3), wantCount: 3, wantOK: true},
		{name: "int16", count: int16(3), wantCount: 3, wantOK: true},
		{name: "int32", count: int32(3), wantCount: 3, wantOK: true},
		{name: "int64", count: int64(3), wantCount: 3, wantOK: true},
		{name: "uint", count: uint(3), wantCount: 3, wantOK: true},
		{name: "uint8", count: uint8(3), wantCount: 3, wantOK: true},
		{name: "uint16", count: uint16(3), wantCount: 3, wantOK: true},
		{name: "uint32", count: uint32(3), wantCount: 3, wantOK: true},
		{name: "uint64", count: uint64(3), wantCount: 3, wantOK: true},
		{name: "uintptr", count: uintptr(3), wantCount: 3, wantOK: true},
		{name: "signed_zero", count: int64(0), wantCount: 0, wantOK: true},
		{name: "signed_negative_one", count: int64(-1)},
		{name: "signed_min", count: int64(math.MinInt64)},
		{name: "signed_max", count: int64(math.MaxInt64), wantCount: math.MaxInt64, wantOK: true},
		{name: "unsigned_zero", count: uint64(0), wantCount: 0, wantOK: true},
		{name: "unsigned_max_int64", count: uint64(math.MaxInt64), wantCount: math.MaxInt64, wantOK: true},
		{name: "unsigned_above_int64_clamps", count: uint64(math.MaxInt64) + 1, wantCount: math.MaxInt64, wantOK: true},
		{name: "unsigned_max_clamps", count: uint64(math.MaxUint64), wantCount: math.MaxInt64, wantOK: true},
		{name: "float_not_usable", count: float64(3)},
		{name: "string_not_usable", count: "3"},
		{name: "bool_not_usable", count: true},
		{name: "typed_nil_pointer_not_usable", count: (*int64)(nil)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			headers := deathHeaders(deathEntry(deathTestQueue, DeathReasonRejected, tc.count))
			count, ok := DeathCount(headers, deathTestQueue, DeathReasonRejected)
			assert.Equal(t, tc.wantCount, count)
			assert.Equal(t, tc.wantOK, ok)
		})
	}
}

func TestDeathCountNeverPanics(t *testing.T) {
	values := []struct {
		name  string
		value any
	}{
		{name: "untyped_nil", value: nil},
		{name: "typed_nil_slice", value: []any(nil)},
		{name: "typed_nil_table", value: amqp.Table(nil)},
		{name: "typed_nil_map", value: map[string]any(nil)},
		{name: "typed_nil_pointer", value: (*amqp.Table)(nil)},
		{name: "nested_slices", value: []any{[]any{[]any{deathEntry(deathTestQueue, DeathReasonRejected, int64(1))}}}},
		{name: "slice_of_scalars", value: []any{int64(1), "rejected", 2.5, true, []byte("q")}},
		{name: "slice_of_nil_entries", value: []any{nil, amqp.Table(nil), map[string]any(nil)}},
		{name: "typed_table_slice", value: []amqp.Table{deathEntry(deathTestQueue, DeathReasonRejected, int64(1))}},
		{name: "wrong_typed_fields", value: []any{amqp.Table{"queue": 1.5, "reason": []any{}, "count": amqp.Table{}}}},
		{name: "nil_fields", value: []any{amqp.Table{"queue": nil, "reason": nil, "count": nil}}},
		{name: "x_death_as_table", value: deathEntry(deathTestQueue, DeathReasonRejected, int64(1))},
		{name: "x_death_as_bytes", value: []byte("x-death")},
	}

	for _, tc := range values {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				count, ok := DeathCount(amqp.Table{headerXDeath: tc.value}, deathTestQueue, DeathReasonRejected)
				assert.Zero(t, count)
				assert.False(t, ok)
			})
		})
	}
}

func TestDeathReasonConstantsMatchBrokerSpelling(t *testing.T) {
	assert.Equal(t, "rejected", DeathReasonRejected)
	assert.Equal(t, "expired", DeathReasonExpired)
	assert.Equal(t, "maxlen", DeathReasonMaxLen)
	assert.Equal(t, "delivery_limit", DeathReasonDeliveryLimit)
}
