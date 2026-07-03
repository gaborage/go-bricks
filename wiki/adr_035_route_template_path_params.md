# ADR-035: Route Template and Path-Parameter Accessors on HandlerContext

**Status:** Accepted
**Date:** 2026-07-02

## Context

[ADR-034](adr_034_echo_boundary_types.md) (v0.45.0, PR #627) replaced the
exported `HandlerContext.Echo` field with stdlib-typed accessors scoped to
"what handlers actually use today". Capability parity with `echo.Context` was
never checked: three routing capabilities — the matched route template
(`Path()`), ordered path parameters (`PathValues()`), and path-parameter
mutation (`SetPathValues()`) — were dropped by **silent omission**. None of
them appears in ADR-034's Alternatives Considered, and there are zero mentions
across the 59 review comments on PR #627. Downstream consumers broke on the
v0.45.0 upgrade (issue #633):

- **Template-keyed registries** — rate-limit buckets, metric labels, and
  authorization tables keyed by the registered route template
  (low-cardinality `/api/cards/:cardId/status`, not the concrete URL).
- **Positional parameter substitution** — rebuilding a downstream path from
  the matched parameters in route-template order.
- **Query-param → path-param promotion middleware** — middleware that injects
  or rewrites path parameters before the handler runs.

Two facts were verified at compile level, from outside package `server`:

1. **Mutation has no public channel.** `Set()`, `SetRequestContext()`, and
   the stdlib `Request().SetPathValue()` are all invisible to `Param(name)`
   and to the `param:"x"` struct-tag binder — the binder read site
   (`bindParamTag` in `server/handler.go`) reads echo's internal `pathValues`
   exclusively.
   The only channel that actually works is `reflect` + `unsafe` against the
   unexported `ectx` field — i.e., leaving this unfixed guarantees downstream
   unsafe hacks on the very escape hatch ADR-034 made deliberately
   unnameable.
2. **The route template is recoverable — but only via unpromised engine
   behavior.** `ctx.Request().Pattern` happens to carry the template because
   echo v5.2.1 (`router.go:1037`) stamps the stdlib field after matching.
   That is engine behavior, not an API promise; blessing it as the supported
   path would recreate exactly the concrete-engine coupling ADR-034 removed.
   It is explicitly **not** the supported path.

## Decision

Add two read accessors and one mutator to `HandlerContext`, plus a neutral
`PathParam` struct, all delegating through the unexported `echoContext()`
escape hatch:

```go
// PathParam is one matched path parameter. Ordered slices preserve
// route-template order.
type PathParam struct {
    Name  string
    Value string
}

// RouteTemplate returns the registered route path that matched this request,
// including any group/base-path prefix (e.g. "/api/cards/:cardId/status").
// It is the template the application registered, NOT the concrete URL
// (use Request().URL.Path for that). Empty before routing completes and on
// unmatched (404) requests; on 405 the engine sets the best-matching route's
// template (engine-defined, not a contract).
func (c HandlerContext) RouteTemplate() string

// PathParams returns the matched path parameters in route-template order.
// The returned slice is a defensive copy: safe to retain past the request;
// mutating it does not affect Param() or struct-tag binding.
func (c HandlerContext) PathParams() []PathParam

// SetPathParams replaces the request's path parameters. Subsequent Param(name)
// calls and param:"name" struct-tag binding observe the new set. The input
// slice is copied; nil clears all parameters. Injected params are
// application-supplied — treat them with the same trust as their source value.
func (c HandlerContext) SetPathParams(params []PathParam)
```

`SetPathParams` is justified by the same argument ADR-034 used to keep the
`SetRequest`/`SetRequestContext` mutators: *"a read-only context would strand
every consumer auth/tenant middleware."* Param-rewriting middleware is
stranded today — there is no public write channel that `Param()` or the
`param:"x"` binder observes.

**The wrapper absorbs echo v5's gotchas at the boundary:**

- **Pooled-array aliasing.** `echo.Context.PathValues()` returns a slice
  header aliasing the `sync.Pool`-recycled context's backing array (reused
  across requests) — `PathParams()` performs a mandatory element-wise copy so
  the returned slice is safe to retain past the request.
- **Nil-input panic.** echo v5.2.1's `Context.SetPathValues` panics on nil
  input; the wrapper always constructs a non-nil `echo.PathValues`
  (`len(params)` may be 0), so `SetPathParams(nil)` clears all parameters
  instead of panicking. Echo copies the input into its pooled array, so the
  temporary slice is safe.
- **Population timing.** Path data is populated only after routing completes:
  pre-route hooks and pure-404 requests see an empty template and no
  parameters; on 405 the engine sets the best-matching route's template —
  engine-defined behavior, documented but not promised as a contract.

**Locked shape decisions:**

1. **Name = `RouteTemplate`** — not `Path` (reads as the concrete URL path,
   inviting confusion with `Request().URL.Path`) and not `RoutePattern`
   (would evoke the stdlib `Request.Pattern` field this ADR declines to
   bless).
2. **`SetPathParams` passes values through verbatim** — no duplicate-name or
   empty-name validation, preserving v0.44 `Echo`-field parity; echo's
   `Param()` is first-match-wins on duplicate names.
3. **405 behavior is documented as engine-defined**, not promised.

## Consequences

**Positive:**
- Purely additive: the `apidiff` gate stays green and the change ships in
  minor v0.46.0 as `feat(server):`, not `feat!:` — no migration for existing
  consumers.
- The three broken consumer classes (template-keyed registries, positional
  substitution, param-promotion middleware) work again through a supported,
  vendor-neutral surface — no `reflect`+`unsafe` hacks against `ectx`.
- Echo's pooled-aliasing and nil-panic sharp edges are absorbed once at the
  boundary instead of being rediscovered by every consumer.

**Negative:**
- A future non-echo engine must support post-route path-parameter
  replacement — `SetPathParams` becomes part of the engine contract alongside
  the read accessors.
- Injected parameters flow into `param:"x"` struct-tag binding for downstream
  handlers; the doc comment carries the trust note (application-supplied —
  treat them with the same trust as their source value).
- `PathParams()` allocates a defensive copy on every call — deliberate
  (correctness under context pooling), and paid only by callers.

## Alternatives Considered

- **Read-only accessors only** (`RouteTemplate` + `PathParams`, with
  promotion middleware passing values via `Set()`/context values): rejected —
  `Param()` and the `param:"x"` binder read sites cannot observe context
  values, so every consumer would have to rewrite every read site to check a
  side channel. The mutation gap is the verified breakage, not a
  hypothetical.
- **A single `Route()` accessor returning a `RouteInfo`-like struct**:
  rejected — it allocates for the dominant template-only case and resurrects
  a shape ADR-034 already rejected by name (the `RouteInfo` wrapper, dropped
  as YAGNI).
- **Docs-only: bless `Request().Pattern`**: rejected — it re-couples
  consumers to unpromised engine behavior (echo stamps the field as an
  implementation detail), recreating the coupling ADR-034 removed, and it
  offers nothing for ordered params or mutation.

## Related

- [Issue #633](https://github.com/gaborage/go-bricks/issues/633) — the
  capability-parity gap report this ADR resolves.
- [ADR-034](adr_034_echo_boundary_types.md) — the echo-free boundary this ADR
  extends; source of the `SetRequest`/`SetRequestContext` mutator precedent
  and of the unexported `echoContext()` escape hatch.
- [PR #627](https://github.com/gaborage/go-bricks/pull/627) — the ADR-034
  implementation (v0.45.0) whose accessor set this ADR completes.
