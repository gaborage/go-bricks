# ADR-121: A Typed Handler's Request Type Must Be a Struct, Checked at Registration

**Status:** Accepted
**Date:** 2026-09-27
**Issue:** #1811

## Context

A typed handler binds each request into its request type `T`: the JSON body is decoded into
`*T`, `param`/`query`/`header` tags overlay struct fields, and the validator checks the result.
[ADR-001](adr_001_enhanced_handler_system.md) makes one struct the complete request context,
but nothing enforced it. A `string`, `*string`, `int64`, `[]Item`, `map[string]string`,
`json.RawMessage`, `[16]byte`, `any` or `**Req` request type registered without complaint, and
no request to it reached the handler. The JSON body step runs first: a request with a JSON
`Content-Type` whose body echo's binder rejected answered 400. Every other request (no body, an
empty or non-JSON body, or a JSON body that decoded into `T`) went on to the tag binder, which
panicked with `reflect: NumField of non-struct type`, and Echo's `Recover` turned the panic into
a 500. `time.Time` is a struct kind, so it passed the binder, but the validator refuses every
type convertible to it, and under the framework's validator the route answered 400 on every
request.

The binding-plan refactor kept this behaviour as a per-request fallback to a legacy
reflect-per-request binder and pinned it with a test. It preserved the panic; it did not
endorse it. A route that can never answer is known at registration, so Fail Fast says it is a
startup failure.

## Decision

- **`RegisterHandler` refuses the type.** `RegisterHandler`, and so `GET`/`POST`/…, panics when
  `T`, after removing one pointer level, is not a struct kind, or is a struct the validator
  refuses: `time.Time` and types convertible to it. That is the test `validator.StructCtx`
  applies, so registration refuses exactly the types validation would refuse on every request.
  `**Req` unwraps once to a pointer and is refused.
- **Before anything is recorded.** The check runs before the route reaches
  `DefaultRouteRegistry` or the router. The message names the method, the full path and the
  type: `server: handler registration failed for GET /api/ids: request type []string must be a
  struct or a pointer to a struct (time.Time excluded); wrap the value in a struct field`.
- **`WrapHandler` refuses it too.** The exported door that bypasses `RegisterHandler` panics when
  the wrapper is built. It knows no route, so its message names the type only.
- **The fallback goes.** Every request binds through the precomputed plan; no request reaches
  the legacy reflect-per-request binder any more.

The panic renders a `reflect.Type`, never a recovered panic value, so
[ADR-081](adr_081_recovered_panic_values_reported_by_type.md) is not engaged. The JOSE route
scan already panics from `RegisterHandler` at registration; this follows it.

## Alternatives considered

**Skip the tag overlay for a non-struct `T` and decode the body into it.** Rejected: the
validator returns `InvalidValidationError` for every non-struct pointer, so the 500 would become
a 400 on every request. Making it answer would need a second request model with no validation,
which ADR-001 does not have.

**A compile-time constraint.** Go generics cannot express "a struct kind" as a constraint.

**Check only in `newRequestProcessor`.** Rejected as the only check: it runs after the descriptor
is recorded, and it cannot name the route. It stays as the `WrapHandler` door.

**Return an error.** `RegisterHandler` has no error result. Adding one would change every call
site for a condition that is a programming error.

## Consequences

- **A service that used to boot now refuses.** A handler with such a request type fails
  startup where every request to the route used to fail: a 500 from the tag binder's panic, a
  400 when echo's binder rejected a JSON body first, or, for `time.Time`, a 400 from the
  validator. Every such route registered through `RegisterHandler` was already unusable, so for
  those the change is when it fails and what the message names. The framework fixes the
  validator of every `RegisterHandler` route; only a `WrapHandler` func mounted on the
  consumer's own echo can meet another. The one exception follows: a `time.Time`-shaped request
  type behind `WrapHandler` on an echo instance whose custom validator accepts it could answer,
  and it now panics when the wrapper is built.
  Migration: wrap the value in a struct field, [migrations.md](migrations.md) `[C69.4]`.
- **Struct request types are unchanged.** A struct, a pointer to one, a defined pointer type and
  `struct{}` register and bind exactly as before.
- **Response types are not judged.**

## References

- [ADR-001](adr_001_enhanced_handler_system.md): the enhanced handler system
- [ADR-081](adr_081_recovered_panic_values_reported_by_type.md): panic values reported by type
- [wiki/handler_patterns.md](handler_patterns.md): request and response type patterns
- `server/handler.go` (`requestStructType`, `RegisterHandler`, `newRequestProcessor`)
