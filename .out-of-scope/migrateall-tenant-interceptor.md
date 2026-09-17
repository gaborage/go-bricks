# MigrateAll per-tenant interceptor (`Prepare` / `Finish`)

**Decision:** Rejected — `migration.MigrateAllOptions` does not grow
`Prepare` / `Finish` callbacks around the per-tenant Flyway call.

**Reason:** The pre-Flyway half already has a seam. `MigrateAll` hands
Flyway exactly the `*config.DatabaseConfig` its `database.DBConfigProvider`
returned, with nothing in between, so a decorator provider is the last word
on a tenant's coordinates: it can overlay fields, validate them, or refuse
the tenant with a caller-owned sentinel that survives `errors.Is` (the loop
wraps it with `%w` as `resolve db config: …`). The `go-bricks-migrate` CLI
already ships one — its TLS-validating provider copies the resolved config,
checks it and fails closed.

```go
type guardedProvider struct{ inner database.DBConfigProvider }

func (p guardedProvider) DBConfig(ctx context.Context, tenantID string) (*config.DatabaseConfig, error) {
	cfg, err := p.inner.DBConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := *cfg // never mutate the inner provider's document
	if err := refuseForeignTarget(&out); err != nil {
		return nil, err // Flyway is never started for this tenant
	}
	return &out, nil
}
```

The post-Flyway half (`Finish`) is where the request breaks down. Its
motivating use is a session-scoped advisory lock that spans the caller's own
DDL and the Flyway subprocess, confirmed before success is announced. That is
a safety protocol, not a progress callback: the lock, the connection that
holds it, the reconciliation before it and the confirmation after it are one
unit, and the ordering between them is the property being protected. Splitting
it across two framework callbacks with no shared handle moves the ordering
guarantee into `MigrateAll`, which the request itself says the framework must
not own ("the seam is the feature; the policies stay with the caller"). A
caller with that protocol is better served by calling `MigrateFor` per tenant
from its own loop, which is supported API and is what the one known caller
with such a protocol deliberately does.

The two concrete needs bundled into the request are handled on their own:
the migrator-identity overlay (#1694) and the fleet verdict that separates
"nothing was attempted" from "a tenant failed" (#1692).

**Reopen when either fires:**

1. A caller needs a post-Flyway per-tenant step that can fail the tenant and
   cannot be expressed as its own `MigrateFor` loop.
2. A second, unrelated caller reimplements listing, `ContinueOnError` and
   parallelism only to get a pre/post pair.

**Prior requests:**

- [#1690](https://github.com/gaborage/go-bricks/issues/1690) — closed
  2026-09-16 (rejected, this entry)
