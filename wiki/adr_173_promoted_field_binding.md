# ADR-173: Typed Handlers Bind Every Promoted URL and Header Field From One Plan

**Status:** Accepted
**Date:** 2026-11-08
**Related:** [ADR-001](adr_001_enhanced_handler_system.md) (binding strategy, §3 amended here) · [ADR-121](adr_121_typed_handler_request_type_must_be_struct.md) (request type must be a struct) · #2202 · #2198

## Context

A typed handler binds path, query and header values through a plan the framework builds once per
route (`buildBindingPlan`). The plan read top-level fields only. Under a JSON Content-Type (any
`+json` type and any letter case since #2198, and every JOSE route, whose decrypted body is decoded
as `application/json`), the JSON step also ran echo's `BindPathValues` and, on GET, DELETE, HEAD and
QUERY, `BindQueryParams` before decoding the body. Echo's binders recurse into value embeds and
named nested structs and match keys case-insensitively. Three properties followed:

- A `param`, `query` or `header` field inside an embedded struct bound only under a JSON
  Content-Type, and stayed empty under any other. Pointer embeds and embeds of an unexported type
  never bound.
- The plan's overlay, which makes a URL value win over the body, never visited an embedded field.
  So on POST, PUT and PATCH the body decode overwrote the path value echo had written, and the
  handler read the body's value for a field tagged `param:"id"`, even with no `json` tag, because
  `encoding/json` matches keys case-insensitively. A check on the raw path parameter and the bound
  field could disagree.
- `param:"ID"` bound route `:id`, and `?PAGE=3` bound `query:"page"`, under JSON only.

## Decision

1. **One plan, every promoted field.** `buildBindingPlan` walks `reflect.VisibleFields`, the fields
   Go promotes to the request struct: top-level fields and those promoted from value embeds,
   pointer embeds and embeds of an unexported type. Each entry carries the field's index path. The
   per-source rules are unchanged: a path value binds when non-empty, a query value when non-empty,
   a header when present, on every method.
2. **The URL and header overlay wins at every depth.** The JSON step only decodes the body. It runs
   no echo side-binding. The plan's overlay runs after it for every promoted field, so a URL or
   header value beats a body value for the same field whatever its depth.
   **Every non-body binding never keeps a body value.** The overlay first zeroes each `param`-,
   `query`- and `header`-tagged field (a nil pointer embed stays nil), then binds non-empty path
   and query values and present headers. An empty path segment, which still matches the route,
   an absent or empty query value and an absent header leave the field zero, so its `validate`
   tags decide; a present-but-empty header binds `""`. Before, the body's value survived there,
   at the top level too, and `encoding/json` fills a field by its Go name even without a `json`
   tag. Headers such as a tenant ID are often trust-bearing, so they follow the same rule.
3. **Pointer embeds are allocated when a value binds into them.** A nil pointer embed on a field's
   path is allocated only when a value binds there; under the header rule a present header is a
   value, even empty. Without a value it stays nil, and the walk never dereferences it.
4. **Keys match exactly.** The plan reads `c.Param(name)`, `c.QueryParam(name)` and
   `Header.Values(name)`. A case-variant path or query key binds under no Content-Type.
5. **Registration refuses what the plan cannot bind.** `RegisterHandler`, and `WrapHandler` when
   the wrapper is built, panic naming the method, the full path, the request type and the field's
   Go path, when:
   - a `param`, `query` or `header` tag sits inside a named (non-embedded) nested struct, at any
     depth, the struct elements of a slice, array or map included. Go does not promote it, so the
     plan would never bind it.
   - such a tag sits on an unexported field. Reflect cannot set it, so it never bound.
   - such a tag sits on a struct-typed field, a tagged embed included (``Paging `query:"paging"` ``).
     `time.Time` itself is a value, not a struct; a type merely convertible to it (`type Stamp time.Time`) is refused, because the binder parses only `time.Time`. Before, this answered 400
     "unsupported struct type", and only when the client sent the key.
   - such a tag sits on a field promoted through an embedded pointer to an unexported type. Reflect
     cannot allocate that pointer.
   - such a tag sits on a field of a type no single string fills: a slice that is not of a
     string kind, a `param`-tagged slice of any kind, a map, an array, a channel or a func.
     Before, this answered 400, and only when the client sent the key.
   - such a tag sits on an embedded field Go does not promote, because a shallower field of the
     same name hides it or another field at its depth makes it ambiguous. The plan would leave it
     zero. The check walks every field, hidden embeds and named nested structs included, so the
     order of the embeds does not change the verdict.
6. **A type implementing `encoding.TextUnmarshaler` whose kind has no plain setter binds through
   it**, at any depth and behind pointers (`uuid.UUID`, a `[N]byte` code type, a `param`-tagged
   string slice). A string-, int- or bool-kind type keeps its plain setter, as a top-level field
   always did. Echo's side-binding honored the interface, and `echo.BindUnmarshaler`, for embedded
   fields under JSON; ADR-034 keeps echo types off the consumer surface, so `BindUnmarshaler` is
   not honored, and an embedded string-kind `TextUnmarshaler` is now assigned plainly.
7. Two visible fields carrying the same tag both bind.

## Alternatives considered

- **Remove echo's side-binding without recursion.** Rejected. Embedded fields would bind under no
  Content-Type, which breaks the documented embedding pattern that go-bricks-openapi already
  renders as spec parameters.
- **Keep echo's binding and re-run the overlay through it.** Rejected. Its behavior depends on the
  Content-Type and the method, it matches keys case-insensitively, and it cannot allocate pointer
  embeds.
- **Recurse into named nested structs.** Rejected. A nested struct is a body shape. Binding URL
  values into it would make its JSON meaning depend on the route.

## Consequences

- **Breaking**:
  - An embedded URL or header field binds under every Content-Type, and its URL or header value
    beats the JSON body.
  - Query values bind into embedded fields on every method.
  - Every `param`-, `query`- or `header`-tagged field, top-level included, is zero when its
    source is empty or absent (a header: absent), where the body's value survived.
  - Case-variant keys no longer bind.
  - A malformed embedded value answers 400 under every Content-Type.
  - A URL or header tag in a named nested struct, on a struct-typed field, on an unexported field,
    on a field of an unbindable type, on a hidden or ambiguous embedded field, or behind an
    unexported pointer embed fails registration.
  - An embedded field whose type implements `echo.BindUnmarshaler`, or a string-, int- or
    bool-kind `encoding.TextUnmarshaler`, no longer runs that method under JSON.
- `wiki/handler_patterns.md` "Binding source precedence" covers promoted fields. ADR-001 §3's
  precedence bullet is amended.
- No signature or configuration key moves. See [migrations.md](migrations.md) `[C73.41]`.
