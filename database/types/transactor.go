// Package types contains the core database interface definitions for go-bricks.
//
//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"context"
	"database/sql"
)

// Transactor defines transaction management operations.
// This interface follows the Single Responsibility Principle by focusing solely on
// transaction lifecycle, separate from query execution, health checks, and migration support.
//
// Transactor is typically used by business logic that needs to execute multiple operations
// atomically (all succeed or all fail). For query-only operations, use the Querier interface.
//
// The database.Interface type embeds Transactor, so all existing code continues to work unchanged.
//
// Usage in services:
//
//	func (s *OrderService) CreateWithPayment(ctx context.Context, order Order, payment Payment) error {
//	    db, err := s.deps.GetDB(ctx)
//	    if err != nil {
//	        return err
//	    }
//
//	    tx, err := db.Begin(ctx)
//	    if err != nil {
//	        return err
//	    }
//	    defer tx.Rollback()  // No-op if already committed
//
//	    if err := s.insertOrder(ctx, tx, order); err != nil {
//	        return err
//	    }
//	    if err := s.insertPayment(ctx, tx, payment); err != nil {
//	        return err
//	    }
//
//	    return tx.Commit()
//	}
//
// For testing transaction logic, see the database/testing package which provides
// TestTx for tracking commit/rollback behavior and query execution within transactions.
type Transactor interface {
	// Begin starts a new transaction with default isolation level.
	// The returned Tx must be committed or rolled back to release resources.
	//
	// Common usage pattern:
	//   tx, err := db.Begin(ctx)
	//   if err != nil { return err }
	//   defer tx.Rollback()  // No-op if already committed
	//   // ... execute operations on tx ...
	//   return tx.Commit()
	Begin(ctx context.Context) (Tx, error)

	// BeginTx starts a new transaction with explicit isolation level and read-only settings.
	// Use this when you need precise control over transaction behavior.
	//
	// Common isolation levels (from database/sql):
	//   - sql.LevelDefault: Use database's default isolation
	//   - sql.LevelReadUncommitted: Lowest isolation, allows dirty reads
	//   - sql.LevelReadCommitted: Prevents dirty reads (PostgreSQL default)
	//   - sql.LevelRepeatableRead: Prevents dirty and non-repeatable reads
	//   - sql.LevelSerializable: Highest isolation, full transaction isolation
	//
	// Example (read-only transaction for complex reporting):
	//   tx, err := db.BeginTx(ctx, &sql.TxOptions{
	//       Isolation: sql.LevelRepeatableRead,
	//       ReadOnly:  true,
	//   })
	//
	// Note: Not all databases support all isolation levels. Consult vendor documentation.
	BeginTx(ctx context.Context, opts *sql.TxOptions) (Tx, error)
}
