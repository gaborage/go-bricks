# ADR-091: The Native Streams Lane Is Opt-In at the Build Graph

- **Status**: Accepted
- **Date**: 2026-08-31
- **Related**: [ADR-059](adr_059_streams_consumption.md) (the lane this hook links) · [ADR-045](adr_045_no_producer_side_manager_interfaces.md) (the manager stays concrete; the seam is a factory, not a second implementation) · [ADR-063](adr_063_streams_native_publishing.md) (publish surface, unchanged) · [ADR-089 reserved](../docs/superpowers/plans/2026-08-29-inbox-per-tenant-hold.md) (hold port, now implemented through the same seam)

## Context

`app` statically imported `messaging/streams` so `prepareRuntime` could start the stream
manager. After `go mod tidy`, every consumer of `app` — and therefore of `inbox`,
`outbox`, `scheduler`, `keystore`, anything that imports `app` — pulled
`rabbitmq-stream-go-client` and its transitive tail (`snappy`, `lz4 v2 +incompatible`,
archived `pkg/errors`, `murmur3`). Most of those services never declare a stream.

A Go build tag would hide the lane behind a compile flag operators have to remember on
every image. A `go.mod` sub-module would split the repository's module graph. Both were
rejected at triage. The remaining option is a **registration hook**: the package that
owns the vendor client registers a factory into a seam `app` already walks; a process
that never imports `messaging/streams` carries none of the client.

`app` tests import `messaging/streams` and live in `package app`. A seam defined *in*
`app` that `messaging/streams` imported would be an import cycle the moment those tests
compiled. The seam therefore lives in `internal/streamruntime`, which both `app` and
`messaging/streams` import and which `inbox` uses for the hold port — one registry, not
a second vendor hook.

## Decision

**The streams lane is present in a process if and only if `messaging/streams` is in the
import graph.**

1. **One seam.** `internal/streamruntime.Register` is the only factory hook. `messaging/streams`
   calls it from `init`. `app.RegisterStreamRuntime` is the same function under the name
   an explicit registration would use. A second registration panics.
2. **Blank import is enough.** `_ "github.com/gaborage/go-bricks/messaging/streams"` links
   the lane. A module that already imports the package to implement `DeclareStreams`
   does not need a second import.
3. **Config present, package not linked → startup error.** `messaging.streams.uri` set
   with no registered runtime returns `app.ErrStreamsNotLinked`, which names the import.
   A leftover URI must not boot as a silent no-op.
4. **Config absent, package not linked → clean start.** No URI and no runtime is the
   fleet-majority case and stays a no-op.
5. **Linked behaviour is unchanged.** Same config keys, the same `DeclareStreams(*streams.Declarations)`
   method, the same manager lifecycle. `app.StreamDeclarer` and
   `ModuleRegistry.DeclareStreams` are removed because they named `*streams.Declarations`;
   collection moves into the registered runtime, which type-asserts
   `streams.StreamDeclarer`.
6. **Hold types live on the seam.** `HeldMessage`, `HoldLedger` and `HoldReplayer` move
   to `internal/streamruntime` and are re-exported from `app` and `messaging/streams` as
   aliases, so `inbox` implements the port without importing the vendor package.

### Why not a build tag or a sub-module

A tag (`streams`) would have to appear on every `go test`, every image, and every
consumer's CI matrix, or the lane would silently vanish. A sub-module would make the
vendor client a separate `require`, but would also split versions, replace directives,
and the tidy graph this repository treats as one module. The registration hook keeps
one module and makes the *import* the opt-in, which is the unit the Go build graph
already understands.

### Why the seam is not a second Manager

ADR-045 forbids an exported manager interface in `messaging/streams`. The concrete
`*streams.Manager` is unchanged. `streamruntime.Handle` is what `app` stores after the
factory returns, so the production package never names the vendor type. Tests in
`package app` still assign a `*streams.Manager` to a narrower field interface
(`Close` / `StopConsumers` / `Ready` / `Stats`) that the concrete type already
implements.

## Consequences

- **Breaking.** `app.StreamDeclarer` and `ModuleRegistry.DeclareStreams` are gone;
  implementers of `DeclareStreams(*streams.Declarations)` do not change, but a type
  assertion on `app.StreamDeclarer` or a direct `DeclareStreams` call on the registry
  stops compiling. A process that sets `messaging.streams.uri` and does not import
  `messaging/streams` now fails startup. See `[C61.25]`.
- A core-only consumer (`app`, `config`, `server`, `messaging`, `outbox`, `inbox`,
  `scheduler`, …) has none of the stream-client modules in its import graph after tidy.
- Stream users who already import `messaging/streams` to declare topology keep working
  with no code change; the `init` registration is the import they already have.
- The hold ledger stays one port. Inbox and the streams runner share the seam types,
  so the aliases are the same type, not a conversion.
