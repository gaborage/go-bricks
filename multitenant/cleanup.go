package multitenant

import (
	"context"
	"errors"
	"fmt"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// RetentionDelete removes rows older than cutoff from the resolved tenant database. It is
// the per-vendor delete a retention-cleanup job runs (e.g. the outbox's DeletePublished or
// the inbox's DeleteProcessed) and matches those store methods' signatures directly.
type RetentionDelete func(ctx context.Context, db dbtypes.Interface, cutoff time.Time) (int64, error)

// FanOutRetentionCleanup runs a retention delete once per tenant, resolving each tenant's
// database via getDB with that tenant injected into the context (SetTenant). Per-tenant
// errors are collected with errors.Join so one unhealthy tenant cannot block the others.
// Single-tenant callers pass tenants=[""] (SetTenant("") is a no-op). name labels the job in
// log and error messages (e.g. "outbox", "inbox").
//
// This is shared by the outbox and inbox cleanup jobs, whose Execute bodies are otherwise
// identical — only the store delete and the label differ.
func FanOutRetentionCleanup(
	ctx context.Context,
	log logger.Logger,
	tenants []string,
	getDB func(context.Context) (dbtypes.Interface, error),
	retention time.Duration,
	name string,
	del RetentionDelete,
) error {
	cutoff := time.Now().Add(-retention)
	var errs []error
	for _, tenantID := range tenants {
		tctx := SetTenant(ctx, tenantID)
		db, err := getDB(tctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s cleanup: tenant %q: database not available: %w", name, tenantID, err))
			continue
		}
		if db == nil {
			errs = append(errs, fmt.Errorf("%s cleanup: tenant %q: database not available", name, tenantID))
			continue
		}

		deleted, err := del(tctx, db, cutoff)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s cleanup: tenant %q: delete failed: %w", name, tenantID, err))
			continue
		}

		if deleted > 0 {
			event := log.Info().
				Int64("deleted", deleted).
				Str("cutoff", cutoff.Format(time.RFC3339))
			if tenantID != "" {
				event = event.Str("tenant", tenantID)
			}
			event.Msg(name + " cleanup completed")
		}
	}
	return errors.Join(errs...)
}
