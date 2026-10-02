# ADR-135: Dead Public Surface Is Deleted, Not Deprecated

**Status:** Accepted
**Date:** 2026-10-01
**Related:** [ADR-070](adr_070_inbound_trace_identifier_validation.md) (the exported trace API and
its detection grep) · [ADR-008](adr_008_database_testing_interface_segregation.md) (the narrow
`messaging.Client`) · [ADR-096](adr_096_typed_publish_door.md) (`messaging.Client` carries no publish)

## Context

A sweep of the exported surface found names that only forward to another exported name, and names
nothing references. Each was put to the deletion test: delete the name and ask what complexity
reappears at its callers. For every name below, none does. A caller spells the canonical name
instead, or writes the literal it was handed.

- **`httpclient` trace names.** The constants `HeaderXRequestID`, `HeaderTraceParent` and
  `HeaderTraceState` aliased `trace`'s own, and `WithTraceID`, `TraceIDFromContext`,
  `EnsureTraceID`, `WithTraceParent`, `TraceParentFromContext`, `WithTraceState`,
  `TraceStateFromContext` and `GenerateTraceParent` were one-line calls into `trace`. No ADR,
  `llms.txt` or wiki page taught the `httpclient` spelling, and the second spelling was not
  harmless. ADR-070 left `trace.WithTraceID` and `trace.EnsureTraceID` exported as the one path its
  validation seam cannot guard, and the published grep for them (`[C60.8]`) matches
  `trace.WithTraceID(` but not `httpclient.WithTraceID(`. The aliases hid exactly the call sites
  the migration guide tells consumers to audit.
- **`httpclient.IsJOSEError`** described itself as "a thin re-export of `jose.IsError` for
  discoverability". Its body was `return jose.IsError(err)`.
- **`testing` constants.** `testing/testconsts.go` exported 46 constants after its logger block:
  bare literals (`"users"`, `"tenant-1"`, `6379`, …) added in #164, with no reference under either
  import alias in use (`testconsts`, `gbtesting`) and no mention in any doc.
- **`messaging.Client.Consume`** forwarded to `ConsumeFromQueue` with zero-valued options and had
  no framework caller. The interface itself is not dead. It is the documented narrow interface of
  ADR-008, with a published second adapter.
- **`cache/testing.AssertOperationCountGreaterThan`** was a one-line forwarder to
  `AssertOperationCountAtLeast`, already marked `Deprecated:` and still exported.

## Decision

Delete each name outright, with no `Deprecated:` window, and keep the canonical one. Signatures
are unchanged; `trace` is `github.com/gaborage/go-bricks/trace`.

| Removed | Replacement |
| --- | --- |
| `httpclient.HeaderXRequestID`, `HeaderTraceParent`, `HeaderTraceState` | the same name in `trace` |
| `httpclient.WithTraceID`, `EnsureTraceID`, `WithTraceParent`, `WithTraceState`, `GenerateTraceParent` | the same name in `trace` |
| `httpclient.TraceIDFromContext`, `TraceParentFromContext`, `TraceStateFromContext` | `trace.IDFromContext`, `trace.ParentFromContext`, `trace.StateFromContext` |
| `httpclient.IsJOSEError` | `jose.IsError` |
| the 46 `testing` constants after the logger block | the literal, inlined |
| `messaging.Client.Consume`, `AMQPClientImpl.Consume` | `ConsumeFromQueue(ctx, messaging.ConsumeOptions{Queue: q})` on `messaging.AMQPClient` |
| `mocks.MockMessagingClient.Consume`, `ExpectConsume`, `ExpectConsumeAny` | `mocks.MockAMQPClient.ExpectConsumeFromQueue`, or the new `ExpectConsumeFromQueueAny(err)` |
| `cache/testing.AssertOperationCountGreaterThan` | `AssertOperationCountAtLeast`, same arguments |

`httpclient` calls `trace` directly. `NewTraceIDInterceptor` and `NewTraceIDInterceptorFor` stay:
they carry behavior. `server` used the `httpclient` spellings only in its handler, logger and
trace-context middleware, and now uses `trace`.

`messaging.Client` keeps `Close` and `IsReady`. The fixtures `NewWorkingMessagingClient`,
`NewFailingMessagingClient` and `NewMessageSimulator` now return `*mocks.MockAMQPClient`, which
embeds `*MockMessagingClient` and still satisfies `messaging.Client`, and set their consume
expectations with `ExpectConsumeFromQueueAny`. A simulator's preloaded messages arrive through
`ConsumeFromQueue`. `NewDisconnectedMessagingClient` is unchanged, and the three logger-level
constants in `testing` stay.

## Alternatives considered

- **Keep each name with a `Deprecated:` marker.** Rejected: a deprecated alias is still a second
  spelling, so the `[C60.8]` grep stays blind to it for as long as it ships. The window does not
  close by itself either: `AssertOperationCountGreaterThan` was already deprecated.
- **Keep them until a major version.** Rejected: GoBricks does not preserve backward compatibility
  in its own API surface. A break is recorded in an ADR and a migrations atom, not shimmed.
- **Unexport instead of delete.** Rejected: once `httpclient` calls `trace` directly nothing calls
  these names, so an unexported copy is dead code. The `testing` constants have no caller to keep.

## Consequences

- **Breaking, compile-caught but for one case.** Every affected site fails to build except the
  fixture type assertion below. `go vet ./...` also names the
  sites in `_test.go` files, where the trace helpers, mocks and test constants mostly live. A file
  that already imports `go.opentelemetry.io/otel/trace` must alias one of the two `trace` packages.
- `trace` is the only package exporting `WithTraceID` and `EnsureTraceID`, so the `[C60.8]` grep
  sees a planted id again unless the consumer imports `trace` under an alias that does not end
  in `trace`.
- `server` no longer imports `httpclient`, in production or in its tests.
- A variable typed `*mocks.MockMessagingClient` that holds one of the three fixtures changes type.
  A type assertion to `*mocks.MockMessagingClient` on one still compiles and fails at run time
  (`[C71.16]`).
- The framework's runtime behavior does not change. Each removal is a second spelling or an unused
  name.

## References

- `trace/trace.go`; `httpclient/interface.go`, `httpclient/client.go`,
  `httpclient/jose_transport.go`; `jose/contenttype.go` (`IsError`)
- `server/handler.go`, `server/logger.go`, `server/trace_context.go`
- `testing/testconsts.go`; `messaging/messaging.go`, `messaging/amqp_client.go`;
  `testing/mocks/messaging.go`, `testing/mocks/amqp.go`; `testing/fixtures/messaging.go`;
  `cache/testing/assertions.go`
- [migrations.md](migrations.md) E71: `[C71.13]` (the `trace` names), `[C71.14]` (`IsJOSEError`),
  `[C71.15]` (the `testing` constants), `[C71.16]` (`Consume`, its mock helpers and the fixtures),
  `[C71.17]` (`AssertOperationCountGreaterThan`)
