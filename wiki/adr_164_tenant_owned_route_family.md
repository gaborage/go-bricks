# ADR-164: A Tenant-Owned Route Family Resolves Its Own Tenant

**Status:** Accepted
**Date:** 2026-11-02
**Issue:** #2166
**Amends:** [ADR-036](adr_036_global_middleware.md)

## Context

With `multitenant.enabled: true`, tenant resolution is one seat on the root chain
(`server/tenant_middleware.go` `tenantMiddlewareEcho`). It covers every request except the health
and ready probes, and answers the framework 400 `Invalid tenant` when it fails. Group and route
middleware run after it, and its resolver comes only from `multitenant.resolver.*`. So no module
code runs before the seat, and no module can supply the tenant source.

Consumers need four shapes the seat cannot serve, lettered as in the
[#2225 resolution](https://github.com/gaborage/go-bricks/issues/2225#issuecomment-6536674090):

- **(b) A catalog key.** A header or path key that a catalog lookup built in module `Init` maps to
  a tenant, for one family.
- **(c1) A verified claim.** A tenant that is a claim of a bearer credential, verified by the
  family's own `auth.Middleware` ([ADR-109](adr_109_bearer_credential_verification.md)).
  Authentication answers first, in the family's own bytes.
- **(c2) A token endpoint.** If a token endpoint is planned, its `client_id` lookup would be the
  resolution.
- **(d) A per-family source.** A tenant source declared per family, at its registration site.

Observed on v0.72.0, with the header resolver reading `X-Tenant-ID` and a group at `/partner/v2`
whose middleware answers 401 without `Authorization`. The seat's answers had not changed when this
ADR was accepted:

```text
GET  /partner/v2/orders   no Authorization, no X-Tenant-ID    -> 400 "Invalid tenant"; group middleware never ran
GET  /partner/v2/orders   no Authorization, X-Tenant-ID: acme -> 401 from the group middleware
POST /oauth/token         no X-Tenant-ID                      -> 400 "Invalid tenant"; the handler is never reached
```

Without a header tenant, the seat answers first: neither the family's authentication nor a token
endpoint's handler runs.

## Decision

`server.TenantOwnedGroup(r, prefix, server.TenantPolicy{Authenticate, Tenant, Deny})` mounts a
route family that resolves its own tenant, after its own authentication when it has one, instead
of passing the root tenant seat. The framework checks and publishes the tenant, the family's
`Deny` writes its client-fault bytes, a leaf guard keeps the seat's skip fail-closed, and startup
refuses global middleware beside a family.

### API

```go
package server

// TenantOwnedGroup mounts a route family; call it once per family in Module.RegisterRoutes,
// after every module Init, so p may close over what Init built. prefix is relative to r.
func TenantOwnedGroup(r RouteRegistrar, prefix string, p TenantPolicy) RouteRegistrar

type TenantPolicy struct {
	Authenticate []MiddlewareFunc // optional; runs first, in order, before any tenant exists
	Tenant       TenantFunc       // required; returns the tenant ID, never writes the response
	Deny         DenyFunc         // optional; the only writer of client-fault bytes; nil means DenyBodyless
}

type TenantFunc func(c HandlerContext) (tenantID string, err error)
type DenyFunc func(c HandlerContext, fault error) error

func DenyBodyless(c HandlerContext, fault error) error // the default Deny (see Faults)

// ResolveForTest runs steps 1-3 and the fault rule on c (from NewHandlerContextForTest)
// without a router. denied is true only when Deny ran; the backstop's error stays unexported.
func (p TenantPolicy) ResolveForTest(c HandlerContext) (tenantID string, denied bool, err error)

type RouteDescriptor struct {
	// ...existing fields...
	TenantOwned bool // see Fail-closed › The flag; audit it in app.Options.PostRegisterRoutes
}

// Third reason, beside ADR-155's RouteConflictReasonDuplicate and RouteConflictReasonReservedMethod.
const RouteConflictReasonTenantOwned RouteConflictReason = "inside a tenant-owned route family"

var ErrTenantFamilyWithGlobalMiddleware = errors.New("tenant-owned route family with global middleware")

func (s *Server) GlobalMiddlewareCount() int     // installed global middleware, after the nil filter
func (s *Server) TenantFamilyPrefixes() []string // each family's full prefix, in build order

package auth

// TenantClaim reads the named claim from the Principal auth.Middleware attached in Authenticate.
func TenantClaim(claim string) server.TenantFunc
```

The smallest family is a bearer token whose `tid` claim names the tenant:

```go
partner := server.TenantOwnedGroup(r, "/partner/v2", server.TenantPolicy{
	Authenticate: []server.MiddlewareFunc{auth.Middleware(m.verifier)},
	Tenant:       auth.TenantClaim("tid"),
})
server.GET(hr, partner, "/orders", m.listOrders)
```

### Per-request order on a family route

The root chain runs first, unchanged, and the tenant seat skips owned routes. Then:

1. `Authenticate`, each step in order. No tenant exists yet.
2. `Tenant`. It runs on every request under the prefix. On an unknown sub-path or a wrong method
   the named router params are empty, so a family whose tenant comes from a path param answers
   with its own `Deny` there.
3. The framework checks the ID with `multitenant.ValidateTenantID` (the fixed grammar
   `^[a-z0-9-]{1,64}$` of [ADR-162](adr_162_tenant_id_grammar_is_fixed.md), so never the
   control-plane key `""`) and publishes it on the request context. The family never calls
   `SetTenant`.
4. A request that passed steps 1-3 on an unknown sub-path or a wrong method gets the framework
   404, the bare prefix included, so an anonymous caller gets the same answer on every path.
5. Middleware added later with `Use`, nested-group middleware, route middleware, binding, JOSE and
   the handler, all after the publish.

### Faults: who answers

Routes outside a family keep the seat's 400 `Invalid tenant`.

**Classification** applies to an error from step 1 or 2. The first rule that matches wins:

1. A 413, a request deadline or a cancellation is never classified. The framework renders it.
2. An error wrapping `multitenant.ErrNoTenant` (`app.ErrNoTenantInContext`) is a server fault,
   whatever status or sentinel it also carries. Calling a per-tenant accessor before the tenant
   exists is a wiring bug, so catalog reads use `deps.DBByName`.
3. An error with an HTTP status is a client fault at 4xx and a server fault at 5xx. "An HTTP
   status" means a `server.IAPIError` or an `echo.HTTPStatusCoder` (`*echo.HTTPError` included),
   mapped the way the framework error handler maps them. So `server.NewUnknownTenantError`
   ([ADR-161](adr_161_unknown_tenant_is_a_client_fault.md)) is a client fault when its cause `Is`
   `ErrUnknownTenant` (400), and a server fault for any other cause (500).
4. An error without a status is a client fault when it wraps
   `multitenant.ErrTenantResolutionFailed` or `multitenant.ErrInvalidTenantID`.
   `multitenant.ErrUnknownTenant` (`multitenant/errors.go`), a standalone sentinel under ADR-161,
   is redefined to wrap `ErrTenantResolutionFailed`, keeping its text, as ADR-161 Decision 1
   records: an unexported error type whose `Error()` is `"multitenant: unknown tenant"` and whose
   `Unwrap()` returns `ErrTenantResolutionFailed`. So a `Tenant` step that returns it is a client
   fault here too. ADR-161's root-chain 400 is unchanged: both checks of the sentinel
   (`classifyError`, `NewUnknownTenantError`) use `errors.Is`, and no production code tested
   `errors.Is(..., ErrTenantResolutionFailed)` before this change, so nothing newly matches.
5. Anything else is a server fault.

**Client faults go to `Deny`**, at any status, 200 included. `traceparent` is stamped before the
call.

- `DenyBodyless` writes no body and keeps a 4xx fault's own status, so `auth.Middleware`'s 401
  keeps the `WWW-Authenticate` header it set. Any other fault gets 403, because a 401 without
  `WWW-Authenticate` breaks RFC 7235. A bodyless-401 contract wraps `DenyBodyless`.
- A token endpoint's pre-tenant errors are client faults too: a missing `grant_type`, a malformed
  Basic header, an unknown `client_id`. No family route gets a tenantless escape.

**The framework renders everything else:** server faults, rule-1 errors, the step-4 404, and every
error raised after step 3, even one passing back up through `Authenticate`.

**Wiring faults.** `Deny` is the only byte writer. Each of these is logged by reason class:

- **`Deny` returns an error.** The framework renders it.
- **`Deny` returns nil without committing a response.** The framework answers 500.
- **An `Authenticate` step commits a response.** The committed bytes stand, because they cannot be
  taken back, and `Tenant` never runs. If the step then calls `next`, `next()` returns nil without
  running anything.
- **An `Authenticate` step returns nil without calling `next` and without committing.** The
  framework answers 500.

**Grammar backstop.** An ID that fails step 3 is a server fault: a 500, logged by reason class and
never by value. It means corrupt operator data, or an adapter that skipped validation. The grammar
is ADR-162's: a `Tenant` step maps a foreign identity into it (ADR-162 Decision 3), and unlike a
caller-built resolver passed to `TenantMiddleware`, no family ID skips the check. An adapter that
reads caller-written input checks it with `multitenant.ValidateTenantID` before any query, and
returns that error as a client fault. `auth.TenantClaim` returns:

- a plain error with no Principal (a wiring fault: 500);
- an error wrapping `ErrTenantResolutionFailed` for an absent, empty or non-string claim;
- an error wrapping `ErrInvalidTenantID` (from `ValidateTenantID`) for a claim that fails the
  grammar.

**`Tenant`** may `c.Set` values for the handler and may return a 4xx to authorize. It never writes
response bytes.

### Fail-closed, behind the seam

- **The seat leaves evidence** of each skip, and skips only because the route is owned.
- **A leaf guard** wraps every family handler, whether typed, raw or nested.
  - It sits inside route middleware and around the typed leaf, so binding and JOSE run after it.
    It is echo-native, with no per-request baton
    ([ADR-026](adr_026_zero_overhead_request_path.md)).
  - It requires the seat's evidence and exactly the tenant published in step 3. Anything else gets
    the framework 500, without running the handler.
  - The 500 is logged with a reason class and the matched template. It never logs the tenant or a
    header value.
- **The seat-skip key** is `c.Path()` at or under an owned prefix on a segment boundary. The
  prefix comes from the family's normalized full template, never from the request URL.
  - The key is sound: [ADR-155](adr_155_reserved_engine_methods_refused.md)'s admission refuses
    `echo_route_not_found` and `echo_route_any` at every framework-built door, and trespass fails
    startup, so every matched route came through the family.
- **Only the tenant seat reads ownership.** `tenantMiddlewareEcho` gains the ownership input; the
  public `TenantMiddleware` passes none.
  - Forwarded-client-cert identity, OTel and the access log keep their probe-only exemption.
  - The IP pre-guard and the rate limiter exempt nothing. The limiter keys every request on the
    client IP, family requests included ([ADR-160](adr_160_rate_limit_keys_on_client_ip.md)).
  - The global-middleware refusal reads two facts at boot, never ownership per request.
- **The global client-certificate check stays authoritative.** With
  `server.forwardedclientcert.require: true`, the root 401 answers before the family runs. It is
  the one client-facing refusal a family does not write, because it is a transport fault.
  - A family that must write even that one runs with `require: false`. That is allowed only when
    every mTLS group carries its own check.
  - The family's check then goes in `Authenticate`, as `server.RequireForwardedClientCert` or its
    own, and `Deny` adds `WWW-Authenticate` if needed.
- **The family is built on the engine at its full prefix**, so parent-registrar middleware never
  runs on it, in either order.
- **Catch-alls and `Use`.**
  - The family's catch-alls carry steps 1-4: an any-method route at `<prefix>`, a not-found
    route at `<prefix>/*`, and a not-found route at each family template ending in a wildcard.
    Nested groups register none of their own.
  - The family's `Use` and nested-group middleware are stored and attached per route, never
    through echo's `Group.Use`. So `Use` reaches every route registered after the call, including
    routes registered through a nested group created before it, and no route registered before it.
- **The flag.** Every route a framework `routeGroup` builds sets `TenantOwned = rg.family != nil`
  (true exactly for a route registered through a family), after `RegisterHandler`'s `RouteOption`
  loop.
  - So no option can forge or clear the flag.
  - A registrar the framework did not build, passed to `RegisterHandler`, is outside the
    guarantee. ADR-155 scopes its own guarantee the same way.

### Boot refusals

`TenantOwnedGroup` panics at its call site when:

- `r` is not a registrar the framework built (`ModuleGroup()`, `RootGroup()` or a `Group` below
  them);
- `p.Tenant` is nil;
- the prefix is `""` or `/`;
- the full prefix has a `:param` or `*` segment;
- the full prefix equals or covers the health or ready path;
- the full prefix equals, covers or sits under another family's prefix, compared on segment
  boundaries (`/partner` overlaps `/partner/v2`; `/partner/v20` does not);
- `r` is already inside a family;
- multitenant is disabled. The panic names the prefix, following
  [ADR-127](adr_127_resource_plan_rule.md)'s precedent. A module shared with single-tenant
  binaries branches on config;
- the Server installed no tenant seat, because its tenant resolver could not be built (a
  multitenant config that skipped `config.Validate`). The leaf guard would otherwise refuse
  every family request.

**Each family belongs to one module:** the one whose `RegisterRoutes` built it. A second family on
an overlapping prefix panics. Any route or middleware-bearing group at or below the prefix that did
not come through the family is trespass, the owning module's plain registrar included.

**Trespass** is a `RouteConflictReasonTenantOwned` conflict, in either registration order. It
travels in `*server.DuplicateRouteError`, whose head reads
`route registration refused (N conflict(s))` ([ADR-155](adr_155_reserved_engine_methods_refused.md)).
So `checkRouteConflicts` fails App startup before `PostRegisterRoutes`, and `Server.Start` refuses
before binding. There are two kinds.

**A route** at or below the prefix that did not come through the family: typed or raw, any method.

- `Method` and `Path` are the route's. `FirstPath` is the owned prefix, `First` is empty, and
  `Duplicate` is the route's registrant.
- A route registered after the family is refused and never added. A route registered before it is
  reported when the family is built.
- Line:
  `<method> <path> — inside a tenant-owned route family at <prefix>, refused: <handler> (<package>)`.

**A group** at or below the prefix that is not one of the family's and carries middleware.

- The middleware can be its own (`Group(prefix, mw...)`), inherited from its parent at creation,
  or added with `Use`, before or after the family.
- `Method` is empty. `Path` is the group's full prefix, `FirstPath` is the owned prefix, and
  `Duplicate.Package` is the package that called `Group` or `Use`.
- `routeGroup.Group` and `routeGroup.Use` gain an admission hook for this.
- Line:
  `group <path> — inside a tenant-owned route family at <prefix>, refused: middleware (<package>)`.

Ancestor groups, `ModuleGroup` and `RootGroup` are allowed, with or without middleware.

`FirstPath` and an empty `Method` take a meaning specific to this reason, so the `RouteConflict`
field doc comments state what each field means under each reason. Under `duplicate` and
`reserved engine method`, the meanings stay unchanged.

Call-site refusals panic. Trespass and the global-middleware refusal are returned errors, because
each depends on two facts that can arrive in either order. A call-site refusal needs only facts
that are all known when `TenantOwnedGroup` runs (its arguments, its registrar, the configuration
and the families already built), so it fails fast where the mistake is written; a two-fact refusal
cannot be decided by whichever fact arrives first, so it is recorded and refused at startup.

### Global middleware beside a family

**Startup refuses a binary that has at least one non-nil global middleware installed and that
builds a tenant-owned family, in either order.** A module that returns no non-nil middleware does
not trigger it.

- **Records.** `GlobalMiddlewareCount()` counts what `RegisterGlobalMiddleware` installed after its
  nil filter, whether it came from a module or from a direct call on an injected server.
  `TenantFamilyPrefixes()` lists the families. `app` reads each through its own one-method
  interface assertion, as it reads `RouteConflicts()`.
- **Attribution (App).**
  - For each module that implements `GlobalMiddleware()`, the App counts the non-nil entries,
    using the server's nil test. A module with at least one is named.
  - The rest of `GlobalMiddlewareCount()` was registered directly on the server.
  - A family belongs to the module whose `RegisterRoutes` was running when
    `TenantFamilyPrefixes()` grew. A family built before that loop has no module.
- **Warning.** When a family exists, startup logs one WARN per module that implements
  `GlobalMiddleware()` and returns no non-nil middleware. Startup continues.

**App check.** It runs in `prepareRuntime`, right after `checkRouteConflicts` and before
`runPostRegisterRoutes`. The decided text, for one family and module-sourced middleware:

```text
tenant-owned route family with global middleware: family /api/v1/orgs (module "orgs"); global middleware from module(s) "audit", "authz"; global middleware runs before a family resolves its tenant, so it would see no tenant on the family's routes — attach each gate per group instead: in the family's Authenticate (before the tenant) or Use (after it), and on every other group that needs it
```

Other shapes change only the first two clauses:

- two or more families: `families /api/v1/orgs (module "orgs"), /partner/v2 (module "partner")`,
  in build order;
- a family with no module: `family /x (no module)`;
- both module and direct middleware:
  `global middleware from module(s) "audit" and 1 registered directly on the server`;
- direct middleware only: `global middleware: 2 registered directly on the server`.

**`Start` backstop.** It sits beside the duplicate-route check and covers a server used without
`app`. Its exact text, for two families and three middleware:

```text
tenant-owned route family with global middleware: 2 family(ies) (/api/v1/orgs, /partner/v2); 3 global middleware installed; global middleware runs before a family resolves its tenant, so it would see no tenant on the family's routes — attach each gate per group instead: in the family's Authenticate (before the tenant) or Use (after it), and on every other group that needs it
```

**Returned errors.** Both refusals return errors that wrap `ErrTenantFamilyWithGlobalMiddleware`.

**Not caught:** a family built after `Start` through a retained registrar. Routes registered that
way share the gap today, so the docs limit the guarantee to binaries that build no family after
`Start`.

### Publishes before the tenant exists

On an owned route, only the family's own `Authenticate`, `Tenant` and `Deny` steps can publish
before step 3. They get no guard. The `TenantPolicy` doc states the behavior. On `Authenticate`:

```go
// No tenant is in context while these steps run, so a publish from one is no
// tenant's event. Under messaging.tenancy: shared it goes out as an unstamped
// control-plane event. Under per-tenant messaging, deps.Messaging fails with
// multitenant.ErrNoTenant (app.ErrNoTenantInContext); returned as is, that is a
// server fault: a wiring bug. Under outbox.tenancy: shared, an AMQP-lane outbox
// publish (RunInSharedTx) also goes out unstamped on the control-plane key,
// whatever messaging.tenancy says; a stream-targeted one fails with
// outbox.ErrStreamTargetRequiresTenant.
```

On `Tenant`:

```go
// The same holds here: the tenant exists only after Tenant returns and the
// framework publishes it. It holds in Deny too, which answers a fault raised
// before any tenant exists.
```

## Consequences

- **Additive, and allocation-free without a family.** No existing exported signature changes and
  no configuration key moves, so the change is a `feat` with no migration step. A binary that
  builds no family keeps today's request path and every allocation baseline, and a non-family
  route beside a family allocates exactly what a plain matched route does.
- **The built-in limiter keys family requests on the client IP,** as it keys every request
  (ADR-160). A family that wants a per-tenant budget adds `server.TenantRateLimit` with `Use`,
  after the publish. In `Authenticate` no tenant exists yet, so it would charge nothing (ADR-160
  Decision 3).
- **Pre-publish publishes are documented, not guarded.** A publish from a family's `Authenticate`,
  `Tenant` or `Deny` step is no tenant's event. The framework adds no guard, so such a publish gets
  what its tenancy mode already does with a tenant-less one: an unstamped control-plane event under
  shared tenancy, `multitenant.ErrNoTenant` under per-tenant messaging. The `TenantPolicy` doc
  states each case.
- **Echo coupling is pinned.** Two echo behaviors carry the design: the seat-skip key is echo's
  matched route path (`c.Path()`), and an unmatched sub-path or method under the prefix lands on
  one of the family's catch-alls. Pin tests cover both, so an echo upgrade that moves either
  fails the suite instead of a family. Echo's `Group.Use` re-registers a catch-all pair, which is
  why the family's `Use` never calls it.
- **A moved gate changes in three ways.** A binary that builds a family cannot keep global
  middleware, so its gates move to groups: on a family, into `Authenticate` (before the tenant) or
  onto `Use` (after it), and onto every other group that needs them. Three things change for a
  moved gate:
  - Framework routes (`/_sys`, `/debug`) lose the consumer's gate. Their own CIDR and bearer
    checks still apply, and so do the root chain's.
  - In `Authenticate`, a 4xx goes to the family's `Deny`, not the standard envelope, except a 413
    (framework-rendered) and an error wrapping `multitenant.ErrNoTenant` (a framework 500). The
    default `DenyBodyless` writes no body. A 5xx, a deadline or a cancel stays framework-rendered.
  - Added with `Use`, it runs after the family's 404, so it never sees an unknown sub-path. It
    also reaches only routes registered after the call.

  A single-tenant binary is unaffected: it cannot build a family.

## Alternatives considered

The options are lettered as in the #2225 resolution. **A**, a route family that owns its tenant
source, is this decision.

- **B: an app-wide resolver supplied in code.** Deferred. It is the only seam for root-mounted
  mapping, and needs the seat's place ahead of the timeout settled first.
- **C** reduces to A.
- **D: config-only.** Rejected: configuration cannot express a catalog lookup built in `Init`, or
  a tenant read from a claim the family's own authentication verified (c1).
- **Variants of A, rejected:**
  - **A two-phase seat.** It publishes the header tenant first, and `SetTenant` then silently
    overwrites it.
  - **A per-group resolver at the seat.** It fails (c1): the seat runs before the family's
    authentication, so no verified claim exists when it resolves.
- **Global middleware beside a family, rejected:**
  - **A splice** of global middleware into the family's pipeline. It needs plumbing, loses the
    short-circuit ahead of `Authenticate`/`Tenant`, and makes a second ownership reader. Run after
    the publish, it stays an additive way to lift the refusal later (see the ADR-036 amendment).
  - **Warn-only.** It fails open: the binary boots, and global middleware runs on the family's
    routes with no tenant in context.
- **Interface shapes.** Raw steps, functional options, a combinator algebra and a typed port were
  compared. The kept shape takes the single call site from the first, the return-valued tenant
  source from the second, and the policy struct with its own `Deny` from the last.

## References

- [ADR-036](adr_036_global_middleware.md): global middleware after tenant resolution; amended here.
- [ADR-026](adr_026_zero_overhead_request_path.md): the request path the leaf guard keeps
  baton-free.
- [ADR-039](adr_039_composite_resolver_order.md): root tenant resolution and its composite chain.
- [ADR-109](adr_109_bearer_credential_verification.md): `auth.Middleware` and the `Principal`
  `auth.TenantClaim` reads.
- [ADR-124](adr_124_server_level_duplicate_route_refusal.md) and
  [ADR-155](adr_155_reserved_engine_methods_refused.md): route admission, `RouteConflict` and
  `*server.DuplicateRouteError`.
- [ADR-127](adr_127_resource_plan_rule.md): the precedent the multitenant-disabled panic follows.
- [ADR-160](adr_160_rate_limit_keys_on_client_ip.md): the client-IP limiter key and
  `server.TenantRateLimit`.
- [ADR-161](adr_161_unknown_tenant_is_a_client_fault.md): `multitenant.ErrUnknownTenant` and
  `server.NewUnknownTenantError`.
- [ADR-162](adr_162_tenant_id_grammar_is_fixed.md): the tenant-ID grammar the backstop applies.
- `server` (`TenantOwnedGroup`, `TenantPolicy`, `DenyBodyless`, `ErrTenantFamilyWithGlobalMiddleware`)
  · `auth` (`TenantClaim`) · `server/tenant_middleware.go` (`tenantMiddlewareEcho`) ·
  `server/route_registrar.go` (`routeGroup`) · `server/route_conflicts.go` (`routeAdmission`,
  `RouteConflict`, `DuplicateRouteError`) · `server/handler.go` (`RegisterHandler`) ·
  `server/global_middleware.go` (`RegisterGlobalMiddleware`) · `server/server.go`
  (`classifyError`, `Start`) · `app/lifecycle.go` (`prepareRuntime`) · `multitenant/errors.go`
  (`ErrUnknownTenant`).
- The [#2225 design resolution](https://github.com/gaborage/go-bricks/issues/2225#issuecomment-6536674090)
  and the [#2280 global-middleware contract](https://github.com/gaborage/go-bricks/issues/2280#issuecomment-6538366546).
- gaborage/go-bricks#2166.
