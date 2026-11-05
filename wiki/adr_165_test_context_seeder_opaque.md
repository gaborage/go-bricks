# ADR-165: `server.TestContextOption` Becomes the Sealed `server.TestContextSeeder`

**Status:** Accepted
**Date:** 2026-11-03
**Issue:** #2212
**Related:** [ADR-013](adr_013_interface_naming_conventions.md) (an exported single-method interface takes
an -er name) · [ADR-034](adr_034_echo_boundary_types.md) (no echo type on the consumer surface) ·
[ADR-133](adr_133_remove_compatibility_shims.md) (`NewHandlerContextForTest` takes
`...TestContextOption`) · [ADR-135](adr_135_dead_public_surface.md) (dead public surface is
deleted, not deprecated)

## Context

`server.NewHandlerContextForTest(w, r, cfg, opts ...TestContextOption)` builds a synthetic
`HandlerContext` for unit tests, and `TestContextOption` was declared as
`func(*HandlerContext)`. The type's shape was therefore public. A consumer could write its own
option as a func literal, convert a func to one with `server.TestContextOption(f)`, or call an
option directly. Since the echo context is unexported, such an option could do nothing a caller
cannot do through `cfg` or on the returned context.

Every option the framework ships, today only `WithRouteTemplate`, also had to be a function that
mutates the finished `HandlerContext`. That ties how an option seeds the synthetic context to the
exported type, so the framework could not change it without a breaking change.

## Decision

**`TestContextOption` is removed and replaced by `TestContextSeeder`, an interface with one
unexported method.** Only types in package `server` can implement it, so the package's
constructors are the only source of a working seeder. The type is renamed as well as sealed
because an exported single-method interface takes an -er name (ADR-013); the break is already
paid for, so the rename rides with it. `WithRouteTemplate` keeps its name and behavior and now
returns `TestContextSeeder`; `NewHandlerContextForTest` keeps its name and takes
`opts ...TestContextSeeder`.

## Alternatives considered

- **Keep the func type.** Rejected. It keeps a consumer-writable door that adds nothing over
  `cfg`, and every change to how an option seeds the context would break consumers.
- **Seal the type and keep the name `TestContextOption`.** Rejected. A single-method interface
  without an -er name violates ADR-013, and renaming later would be a second break.
- **A struct with an unexported field.** Rejected. Its zero value compiles anywhere and would need
  a silent no-op branch.
- **Deprecate the func form first.** Rejected. The project removes an obsolete path outright
  rather than keep a compatibility shim.

## Consequences

- Breaking at compile time only; see C73.27. Every spelling of `server.TestContextOption` stops
  compiling, including a consumer helper's `opts ...server.TestContextOption` parameter, which
  becomes `opts ...server.TestContextSeeder`.
- A consumer struct that embeds `server.TestContextSeeder` still satisfies the interface, but it
  carries a nil seeder that panics when `NewHandlerContextForTest` applies it.
- `TestTestContextSeederIsOpaque` in `server/handler_test.go` fails if the type stops being an
  interface or gains an exported method.
- No configuration key and no production code path changes.

## References

- `server/handler.go`: `TestContextSeeder`, `WithRouteTemplate`, `NewHandlerContextForTest`
- [testing.md](testing.md#server--handlercontext-testing)
- See [migrations.md](migrations.md) `[C73.27]`.
