//go:build integration

package messaging

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const deathCycleTimeout = 10 * time.Second

// deathCycleTopology declares a work queue that dead-letters through the default
// exchange to a retry queue whose TTL dead-letters back to the work queue.
func deathCycleTopology(t *testing.T, ch *amqp.Channel, workType string) (work, retry string) {
	t.Helper()
	work = uniqueName(t, "deathcount-work-"+workType)
	retry = uniqueName(t, "deathcount-retry-"+workType)
	t.Cleanup(func() {
		_, _ = ch.QueueDelete(work, false, false, false)
		_, _ = ch.QueueDelete(retry, false, false, false)
	})
	_, err := ch.QueueDeclare(work, true, false, false, false, amqp.Table{
		argQueueType:            workType,
		argDeadLetterExchange:   "",
		argDeadLetterRoutingKey: retry,
	})
	require.NoError(t, err)
	_, err = ch.QueueDeclare(retry, true, false, false, false, amqp.Table{
		argQueueType:            QueueTypeClassic,
		"x-message-ttl":         int32(50),
		argDeadLetterExchange:   "",
		argDeadLetterRoutingKey: work,
	})
	require.NoError(t, err)
	return work, retry
}

func nextDelivery(t *testing.T, deliveries <-chan amqp.Delivery) amqp.Delivery {
	t.Helper()
	select {
	case d := <-deliveries:
		return d
	case <-time.After(deathCycleTimeout):
		t.Fatal("timed out waiting for a delivery on the work queue")
		return amqp.Delivery{}
	}
}

func logXDeathShape(t *testing.T, label string, headers amqp.Table) {
	t.Helper()
	raw := headers[headerXDeath]
	t.Logf("%s: x-death %T", label, raw)
	entries, _ := raw.([]any)
	for i, e := range entries {
		t.Logf("%s: entry[%d] %T", label, i, e)
		if entry, ok := e.(amqp.Table); ok {
			for k, v := range entry {
				t.Logf("%s: entry[%d].%s = %v (%T)", label, i, k, v, v)
			}
		}
	}
}

func TestDeathCountAccumulatesAcrossDeadLetterCycles(t *testing.T) {
	brokerURL := setupTestBroker(t)
	conn, err := amqp.Dial(brokerURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	for _, workType := range []string{QueueTypeClassic, QueueTypeQuorum} {
		t.Run(workType, func(t *testing.T) {
			ch, err := conn.Channel()
			require.NoError(t, err)
			t.Cleanup(func() { _ = ch.Close() })
			work, retry := deathCycleTopology(t, ch, workType)

			deliveries, err := ch.Consume(work, "", false, false, false, false, nil)
			require.NoError(t, err)
			require.NoError(t, ch.PublishWithContext(context.Background(), "", work, false, false,
				amqp.Publishing{Body: []byte("cycle")}))

			first := nextDelivery(t, deliveries)
			_, ok := DeathCount(first.Headers, work, DeathReasonRejected)
			assert.False(t, ok, "a fresh message carries no x-death")
			require.NoError(t, first.Nack(false, false))

			for cycle := int64(1); cycle <= 3; cycle++ {
				d := nextDelivery(t, deliveries)
				if cycle == 1 {
					logXDeathShape(t, workType, d.Headers)
				}
				count, ok := DeathCount(d.Headers, work, DeathReasonRejected)
				t.Logf("%s cycle %d: DeathCount(work, rejected) = %d, %v", workType, cycle, count, ok)
				assert.True(t, ok)
				assert.Equal(t, cycle, count)
				expired, ok := DeathCount(d.Headers, retry, DeathReasonExpired)
				assert.True(t, ok)
				assert.Equal(t, cycle, expired)
				require.NoError(t, d.Nack(false, false))
			}
		})
	}
}

func TestDeathCountForgedHeaderIsDiscardedOnDeadLetter(t *testing.T) {
	brokerURL := setupTestBroker(t)
	conn, err := amqp.Dial(brokerURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	const forgedQueue = "deathcount-forged-elsewhere"
	for _, workType := range []string{QueueTypeClassic, QueueTypeQuorum} {
		t.Run(workType, func(t *testing.T) {
			ch, err := conn.Channel()
			require.NoError(t, err)
			t.Cleanup(func() { _ = ch.Close() })
			work, _ := deathCycleTopology(t, ch, workType)

			deliveries, err := ch.Consume(work, "", false, false, false, false, nil)
			require.NoError(t, err)
			forged := amqp.Table{headerXDeath: []any{
				amqp.Table{"queue": work, "reason": DeathReasonRejected, "count": int64(99), "exchange": "", "routing-keys": []any{work}},
				amqp.Table{"queue": forgedQueue, "reason": DeathReasonExpired, "count": int64(42), "exchange": "", "routing-keys": []any{forgedQueue}},
			}}
			require.NoError(t, ch.PublishWithContext(context.Background(), "", work, false, false,
				amqp.Publishing{Headers: forged, Body: []byte("forged")}))

			asPublished := nextDelivery(t, deliveries)
			count, ok := DeathCount(asPublished.Headers, work, DeathReasonRejected)
			t.Logf("%s forged before any dead-letter: DeathCount = %d, %v", workType, count, ok)
			assert.True(t, ok, "the broker delivers a publisher-written x-death untouched")
			assert.Equal(t, int64(99), count)
			require.NoError(t, asPublished.Nack(false, false))

			afterCycle := nextDelivery(t, deliveries)
			logXDeathShape(t, workType+"_forged_after_cycle", afterCycle.Headers)
			count, ok = DeathCount(afterCycle.Headers, work, DeathReasonRejected)
			t.Logf("%s forged after one dead-letter cycle: DeathCount = %d, %v", workType, count, ok)
			assert.True(t, ok)
			assert.Equal(t, int64(1), count, "the broker discards a publisher-written x-death when it dead-letters")
			_, ok = DeathCount(afterCycle.Headers, forgedQueue, DeathReasonExpired)
			t.Logf("%s forged unrelated entry after one dead-letter cycle: present = %v", workType, ok)
			assert.False(t, ok, "no forged entry survives the broker's dead-letter")
			require.NoError(t, afterCycle.Ack(false))
		})
	}
}
