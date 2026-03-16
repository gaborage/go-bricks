// Package outbox provides a transactional outbox pattern for reliable event publishing.
//
// The transactional outbox solves the dual-write problem in microservices:
// events are written to an outbox table in the SAME database transaction as business data,
// then reliably delivered to the message broker by a background relay.
//
// This guarantees at-least-once delivery: events are never lost even if the broker
// is temporarily unavailable. Consumers MUST be idempotent — use the x-outbox-event-id
// header for deduplication.
//
// Usage:
//
//	func (m *Module) Init(deps *app.ModuleDeps) error {
//	    m.outbox = deps.Outbox
//	    return nil
//	}
//
//	func (s *Service) CreateOrder(ctx context.Context, order Order) error {
//	    tx, err := db.Begin(ctx)
//	    if err != nil { return err }
//	    defer tx.Rollback(ctx)
//
//	    tx.Exec(ctx, "INSERT INTO orders ...", args...)
//	    s.outbox.Publish(ctx, tx, &app.OutboxEvent{
//	        EventType:   "order.created",
//	        AggregateID: "order-123",
//	        Payload:     payload,
//	        Exchange:    "order.events",
//	    })
//	    return tx.Commit(ctx)
//	}
package outbox
