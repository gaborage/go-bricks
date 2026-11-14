package messaging

import (
	"math"
	"reflect"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Dead-letter reasons RabbitMQ writes into an x-death entry's "reason" field.
const (
	DeathReasonRejected      = "rejected"
	DeathReasonExpired       = "expired"
	DeathReasonMaxLen        = "maxlen"
	DeathReasonDeliveryLimit = "delivery_limit"
)

const headerXDeath = "x-death"

// DeathCount returns the count of the first x-death entry whose queue and
// reason both match, so a handler can bound how often a message cycles through
// a dead-letter retry route. Entries may be amqp.Table or map[string]any, queue
// and reason string or []byte, and count any integer kind; an unsigned count
// above math.MaxInt64 clamps to it. It reports (0, false) when the header is
// absent or not an array, when no entry matches, or when the first matching
// entry's count is missing, negative, or not an integer. It never panics.
//
// The header is publisher-writable, so a cap built on DeathCount bounds honest
// redelivery; it is not a security control. Classic AMQP lane only: a stream
// message carries no x-death.
func DeathCount(headers amqp.Table, queue, reason string) (int64, bool) {
	entries, isArray := headers[headerXDeath].([]any)
	if !isArray {
		return 0, false
	}
	for _, raw := range entries {
		entry, isTable := deathEntryTable(raw)
		if !isTable {
			continue
		}
		if !deathFieldEquals(entry["queue"], queue) || !deathFieldEquals(entry["reason"], reason) {
			continue
		}
		return deathCountValue(entry["count"])
	}
	return 0, false
}

func deathEntryTable(raw any) (map[string]any, bool) {
	switch entry := raw.(type) {
	case amqp.Table:
		return entry, true
	case map[string]any:
		return entry, true
	}
	return nil, false
}

func deathFieldEquals(field any, want string) bool {
	switch value := field.(type) {
	case string:
		return value == want
	case []byte:
		return string(value) == want
	}
	return false
}

func deathCountValue(raw any) (int64, bool) {
	value := reflect.ValueOf(raw)
	if value.CanInt() {
		count := value.Int()
		if count < 0 {
			return 0, false
		}
		return count, true
	}
	if value.CanUint() {
		count := value.Uint()
		if count>>63 != 0 {
			return math.MaxInt64, true
		}
		return int64(count), true
	}
	return 0, false
}
