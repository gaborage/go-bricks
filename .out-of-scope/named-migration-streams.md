# Named migration streams

**Decision:** Rejected — the framework does not add a `Stream{Name,
ConfigPath, MigrationPath}` type, a stream registry, or a CLI `--stream`
preset.

**Reason:** A "stream" is a Flyway conf plus a locations directory, and
`migration.Config` already carries both. A second SQL tree against the same
vendor needs no new API:

```go
tenant := &migration.Config{ConfigPath: "flyway/tenant.conf", MigrationPath: "migrations/tenant"}
control := *tenant
control.ConfigPath, control.MigrationPath = "flyway/control.conf", "migrations/control"

// tenant-shaped: the fleet loop
migration.MigrateAll(ctx, migrator, lister, provider, migration.ActionValidate,
	migration.MigrateAllOptions{BaseConfig: tenant})

// single database: one call, any verb
migrator.InfoFor(ctx, controlDB, &control)
```

`MigrateFor` / `ValidateFor` / `InfoFor` bypass the per-vendor default
entirely when handed a non-nil `*Config`, and `MigrateAllOptions.BaseConfig`
overrides it per field. The CLI reaches the same place: `--flyway-config` and
`--migrations-dir` apply to `migrate`, `validate` and `info` alike, and
`--tenant <id>` runs a single database, so read-only verbs on a non-default
tree need no hand-rolled Flyway invocation.

What a named type would add is a label and an indivisible pair. The label
already has a home (`Config.Audit.Target`), and the framework would own no
behaviour keyed on the name — the request itself says the names stay with the
caller. That is an alias, not a mechanism.

The real gap is documentation: there is no two-tree recipe, and nothing warns
that `BaseConfig` merges per field, so supplying only `ConfigPath` silently
pairs a custom conf with the vendor-default `migrations/<vendor>` directory.
Both are tracked in #1705 rather than as new API.

**Reopen when either fires:**

1. One invocation must run several trees in order (a `[]Stream` — the only
   capability two `Config` values cannot express).
2. The framework grows behaviour that depends on which tree is running.

**Prior requests:**

- [#1693](https://github.com/gaborage/go-bricks/issues/1693) — closed
  2026-09-16 (rejected, this entry)
