# ADR-139: The Sealing Seam Is Framework-Only

**Status:** Accepted
**Date:** 2026-10-03
**Amends:** [ADR-097](adr_097_sealed_amqp_messages.md) (the adapter wiring: registration, configuration and the module read of the runtime), [ADR-131](adr_131_sealed_bytes_publish_door.md) (the external `Sealer` implementer and the verifier/opener providers it names)
**Related:** [ADR-091](adr_091_streams_opt_in_registration.md) (the root-`internal/` seam precedent) · [ADR-136](adr_136_app_hypothetical_seams.md) (the same deletion of an exported runtime door) · [ADR-096](adr_096_typed_publish_door.md) (`internal/publishdoor`, a separate seam this one does not merge into)

## Context

The seal-runtime seam (ADR-097) lived in `messaging/internal/sealruntime`. Go's `internal`
rule let only packages under `messaging/` import it, and `app` could not, so `messaging`
re-exported the seam: three functions, thirteen type aliases and three constants. Every package
in the build could call them. The attacker here is code already in the build, such as a module
or a dependency; no remote input reaches these hooks, so this is hardening, not an exploit.

1. **`messaging.RegisterSealCodec`.** Any package could install the codec. Startup failed
   (`ErrSealingNotLinked`) only when none was registered, so a build that registered its own
   codec instead of blank-importing `messaging/sealed` passed `Declarations.Validate`. A
   replacement sealer could ship a seal-tagged Subject in plaintext as `application/jose`, or
   put arbitrary bytes on the wire past the check `PublishSealed` enforces (ADR-131). A
   replacement opener could accept unsealed bodies and choose the `jti` and sign family of the
   sealed Dedup key `inbox.ProcessOnce` admits, contradicting "no accept-unsealed mode".
2. **The alias family.** `SealCodec`, `SealSpec`, `Sealer`, `SealOpener`, `SealOpenerProvider`,
   `SealVerifier`, `SealVerifierProvider`, `SealRuntime`, `SealTenantRule` and `SealEnvelope`
   made the codec contract implementable outside the module. `SealKeyStore` had no caller, and
   `SealTenancy` with its three constants was carried only by `SealRuntime`.
3. **`messaging.ConfigureSealing`.** The seam documents that a later call replaces the facts, and
   each seal-tagged declaration reads them when it is declared: the producer binds its Activation
   (`messaging.seal.active`) and key store, the consumer builds its Accept set resolver and fixes
   its `tid` rule from the tenancy. A module calling it inside its own `DeclareMessaging` re-aimed
   every seal-tagged handle declared after it, for the life of the process, to a key store of its
   choice, and could switch the `tid` rule off. Every call also rebuilt the seal instruments, so a
   call at any time could turn the seal metrics into no-ops.
4. **`messaging.SealingRuntime()`.** ADR-097 said modules read the runtime through it. Only
   `app`'s own tests called it, and every fact it returned is already on `ModuleDeps`.

## Decision

Move the seam to the root `internal/sealruntime`, with its name, contents and semantics
unchanged, and delete every exported door. ADR-091 created `internal/streamruntime` in root
`internal/` from the start because `app` and the adapter both import it; this ADR moves
`sealruntime` there for the same reason. Go's `internal` rule now makes the seam unreachable from
outside go-bricks, and the seam still imports nothing from `jose` or `keystore`, so `messaging`
stays jose-free (`TestMessagingStaysJoseFree`'s direct-import walk now covers the seam's files).

- `messaging/sealed`'s `init` calls `sealruntime.Register`, so the blank import is the only
  registration. The app's bootstrap calls `sealruntime.Configure` at the same point (before
  `DeclareMessaging`) with the same facts: key store, `messaging.seal.active`, tenancy, meter.
- **Deleted from `messaging`, with no shim and no `Deprecated:`:** `RegisterSealCodec`,
  `ConfigureSealing`, `SealingRuntime`; the aliases `SealCodec`, `SealSpec`, `Sealer`,
  `SealOpener`, `SealOpenerProvider`, `SealVerifier`, `SealVerifierProvider`, `SealRuntime`,
  `SealKeyStore`, `SealTenancy`, `SealTenantRule`, `SealEnvelope`; the constants
  `SealTenancyDisabled`, `SealTenancyShared`, `SealTenancyPerTenant`.
- **Kept:** `SealOpenRefusedError`, now an alias of the root-internal type, because consumers
  reach it with `errors.As` in a sealed delivery's `PayloadError` chain and in `PublishSealed`'s
  `ErrSealedBytesRejected` chain; `ErrSealingNotLinked`, the startup sentinel matched with
  `errors.Is`. `SealedEnvelope`, `SealedEventPublisher`, `Metadata.Sealed` and the rest of the
  sealed surface are not aliases and do not change.

Wire bytes, seal metrics, `SEAL_*` codes, the Validate-time errors and the `messaging.seal.active`
key are unchanged for a correctly wired service.

## Alternatives considered

- **Keep the names with `Deprecated:`.** Rejected: there is no migration target to deprecate
  toward, and the doors stay open for the whole window.
- **Make `Configure` write-once and keep an exported door.** Rejected: tests need a per-case
  reset, and an exported write door would remain.
- **Amend ADR-097 instead of a new ADR.** Rejected: the break spans ADR-096, ADR-097 and ADR-131
  and needs its own migration atom.

## Consequences

- Compile-breaks, caught by `go build`, for code that named any deleted name
  ([migrations.md](migrations.md) `[C73.1]`). A custom codec has no replacement; a module that
  read `SealingRuntime()` reads `ModuleDeps.KeyStore`, `ModuleDeps.MeterProvider` and
  `ModuleDeps.Config`.
- A consumer can no longer exercise the sealed consume door outside a running App. Producer-side
  tests inject `messaging/testing.CaptureSealedPublisher[T]`; handler logic still unit-tests with
  plain JSON bodies, but `Metadata.Sealed()` and the sealed Dedup key need a broker-backed test
  that runs an App with `keystore.NewModule()` fixture keys and the blank import.
- The inbox dedup tests keep their stub codec, registered and configured through the
  root-internal seam, and `inbox` still does not link `messaging/sealed`.
- **Two re-aim paths stay OPEN, tracked in #2010.** This ADR closes the codec half and every
  exported sealing door; it does not establish that no code outside the framework can replace
  the seal runtime facts, and it picks no mechanism for the rest:
  - **Shared `ModuleDeps` writes.** The bootstrap reads the key store and meter from the
    registry's `*ModuleDeps`, and the Activation from the `*config.Config` modules also receive,
    after every module's `Init`. A direct `deps.KeyStore = x`, `deps.MeterProvider = nil` or a
    write to `deps.Config.Messaging.Seal.Active` in `Init` bypasses the single-provider guard and
    reaches sealing. Tenancy is not reachable this way.
  - **A second App's `Run`.** A module that builds another App with a `KeyStoreProvider` and
    calls `Run` reconfigures the process-global runtime, tenancy included, for every seal-tagged
    handle declared afterward.

## References

- [migrations.md](migrations.md) `[C73.1]`
- `internal/sealruntime/sealruntime.go`, `app/sealing.go` (`configureSealing`),
  `messaging/sealed` (`init`), #1872, #2010
