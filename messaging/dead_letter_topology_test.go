package messaging

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	dltQueue   = "orders"
	dltDLX     = "orders.dead"
	dltParking = "orders.parked"
	// dltArgValue is a queue argument value no dead-letter error may quote.
	dltArgValue = "do-not-render-me"
)

// rawDeadLetterQueue registers a queue through the raw-Args escape hatch: dlx and key are stored
// as given, and key is omitted when nil.
func rawDeadLetterQueue(d *Declarations, name string, dlx, key any) {
	queue := NewQueue(name)
	queue.Args[argDeadLetterExchange] = dlx
	if key != nil {
		queue.Args[argDeadLetterRoutingKey] = key
	}
	queue.Args["x-custom"] = dltArgValue
	d.RegisterQueue(queue)
}

// declareDeadLetterExchange registers dlx with exchangeType and a parking queue bound with keys.
func declareDeadLetterExchange(d *Declarations, exchangeType string, keys ...string) {
	d.RegisterExchange(newDurableExchange(dltDLX, exchangeType))
	d.DeclareQueue(dltParking)
	for _, key := range keys {
		d.DeclareBinding(dltParking, dltDLX, key)
	}
}

func TestValidateDeadLetterTopology(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(d *Declarations)
		// want lists the fragments the error must carry; empty means Validate passes.
		want []string
	}{
		{name: "topic_dlx_only_empty_key_binding", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeTopic, "")
			rawDeadLetterQueue(d, dltQueue, dltDLX, nil)
		}, want: []string{`queue "orders"`, `topic exchange "orders.dead"`, "dead-letter rule 6"}},
		{name: "dlq_helper_dlx_retyped_topic", arrange: func(d *Declarations) {
			d.DeclareQueueWithDLQ(dltQueue, nil)
			d.Exchanges[dltQueue+".dlx"].Type = ExchangeTypeTopic
		}, want: []string{`queue "orders"`, `topic exchange "orders.dlx"`, "dead-letter rule 6"}},
		{name: "dlx_absent_from_set", arrange: func(d *Declarations) {
			rawDeadLetterQueue(d, dltQueue, dltDLX, nil)
		}, want: []string{`queue "orders"`, `exchange "orders.dead", absent from this declaration set`, "DeclareExternalExchange", "dead-letter rule 2"}},
		{name: "fanout_dlx_unbound", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeFanout)
			rawDeadLetterQueue(d, dltQueue, dltDLX, nil)
		}, want: []string{`queue "orders"`, `fanout exchange "orders.dead"`, "dead-letter rule 4"}},
		{name: "direct_dlx_key_unmatched", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeDirect, "dead.payments")
			rawDeadLetterQueue(d, dltQueue, dltDLX, "dead.orders")
		}, want: []string{`queue "orders"`, `direct exchange "orders.dead"`, `routing key "dead.orders"`, "dead-letter rule 5"}},
		{name: "topic_dlx_key_unmatched", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeTopic, "dead.*.eu")
			rawDeadLetterQueue(d, dltQueue, dltDLX, "dead.orders")
		}, want: []string{`queue "orders"`, `topic exchange "orders.dead"`, `routing key "dead.orders"`, "dead-letter rule 5"}},

		{name: "dlq_helper_nil_spec", arrange: func(d *Declarations) {
			d.DeclareQueueWithDLQ(dltQueue, nil)
		}},
		{name: "dlq_helper_routing_key", arrange: func(d *Declarations) {
			d.DeclareQueueWithDLQ(dltQueue, &DeadLetterSpec{RoutingKey: "dead.orders"})
		}},
		{name: "dlq_helper_shared_dlx", arrange: func(d *Declarations) {
			spec := &DeadLetterSpec{Exchange: "shared.dlx", ParkingQueue: "shared.dlq"}
			d.DeclareQueueWithDLQ("orders", spec)
			d.DeclareQueueWithDLQ("payments", spec)
		}},
		{name: "direct_dlx_bound_with_queue_name", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeDirect, dltQueue)
			rawDeadLetterQueue(d, dltQueue, dltDLX, nil)
		}},
		{name: "topic_dlx_bound_with_hash", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeTopic, "#")
			rawDeadLetterQueue(d, dltQueue, dltDLX, nil)
		}},
		{name: "topic_dlx_pattern_matches_key", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeTopic, "dead.*")
			rawDeadLetterQueue(d, dltQueue, dltDLX, "dead.orders")
		}},
		{name: "direct_dlx_key_equal", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeDirect, "dead.orders")
			rawDeadLetterQueue(d, dltQueue, dltDLX, "dead.orders")
		}},
		{name: "external_dlx_unbound", arrange: func(d *Declarations) {
			d.DeclareExternalExchange(dltDLX)
			rawDeadLetterQueue(d, dltQueue, dltDLX, "dead.orders")
		}},
		{name: "headers_dlx", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeHeaders)
			rawDeadLetterQueue(d, dltQueue, dltDLX, nil)
		}},
		{name: "plugin_dlx", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, "x-consistent-hash")
			rawDeadLetterQueue(d, dltQueue, dltDLX, nil)
		}},
		{name: "alternate_exchange_dlx", arrange: func(d *Declarations) {
			exchange := newDurableExchange(dltDLX, ExchangeTypeTopic)
			exchange.Args[argAlternateExchange] = "orders.unrouted"
			d.RegisterExchange(exchange)
			d.RegisterExchange(newDurableExchange("orders.unrouted", ExchangeTypeFanout))
			rawDeadLetterQueue(d, dltQueue, dltDLX, "dead.orders")
		}},
		{name: "default_exchange_dlx", arrange: func(d *Declarations) {
			rawDeadLetterQueue(d, dltQueue, "", "orders.retry")
		}},
		{name: "non_string_dlx", arrange: func(d *Declarations) {
			rawDeadLetterQueue(d, dltQueue, 42, nil)
		}},
		{name: "non_string_routing_key", arrange: func(d *Declarations) {
			declareDeadLetterExchange(d, ExchangeTypeDirect, "dead.payments")
			rawDeadLetterQueue(d, dltQueue, dltDLX, []byte("dead.orders"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDeclarations()
			tc.arrange(d)
			err := d.Validate()
			if len(tc.want) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, fragment := range tc.want {
				assert.Contains(t, err.Error(), fragment)
			}
			assert.NotContains(t, err.Error(), dltArgValue, "no Args value but the DLX name and the dead-letter key")
		})
	}
}

func TestValidateDeadLetterTopologyReportsEveryQueueInSortedOrder(t *testing.T) {
	d := NewDeclarations()
	declareDeadLetterExchange(d, ExchangeTypeTopic, "")
	rawDeadLetterQueue(d, "zeta", dltDLX, nil)
	rawDeadLetterQueue(d, "alpha", "missing.dlx", nil)

	err := d.Validate()
	require.Error(t, err)
	text := err.Error()
	alpha, zeta := strings.Index(text, `queue "alpha"`), strings.Index(text, `queue "zeta"`)
	require.NotEqual(t, -1, alpha, "names alpha")
	require.NotEqual(t, -1, zeta, "names zeta")
	assert.Less(t, alpha, zeta, "sorted queue order")
	assert.NotContains(t, text, dltArgValue)
}

// TestValidateDeadLetterAbsentExchangeSharesTheReferenceRemedy pins the absent-DLX remedy to the
// dangling binding reference's text.
func TestValidateDeadLetterAbsentExchangeSharesTheReferenceRemedy(t *testing.T) {
	binding := NewDeclarations()
	binding.DeclareQueue(dltParking)
	binding.DeclareBinding(dltParking, dltDLX, "")
	refErr := binding.Validate()
	require.Error(t, refErr)
	_, remedy, found := strings.Cut(refErr.Error(), "absent from this declaration set")
	require.True(t, found)

	d := NewDeclarations()
	rawDeadLetterQueue(d, dltQueue, dltDLX, nil)
	err := d.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absent from this declaration set"+remedy)
}
