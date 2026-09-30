# ADR-131: A Verified Door Republishes Stored Sealed Bytes, and `Seal` Returns Its `jti`

**Status:** Accepted — `Seal`'s `jti` return ships in this change; the `PublishSealed` door lands in the next link of the #1869 stack
**Date:** 2026-09-30
**Amends:** [ADR-096](adr_096_typed_publish_door.md) (a second, narrow exported path to the wire), [ADR-097](adr_097_sealed_amqp_messages.md) (the caller-side-retry residual, and the rotation drain gate and sign-family step 5 in [sealing.md's rotation runbooks](sealing.md#rotation-runbooks))

## Context

ADR-096 removed every exported byte publish method. A module reaches the broker through
`Publisher[T].Publish` alone, and the one sanctioned bytes path is an outbox row holding what
`Publisher[T].Seal` produced. It rejected a raw escape hatch: byte-level interop "re-earns a
narrow door through its own ADR with its own threat model". ADR-097 seals once per `Publish`
call, before the retry loop, so a caller-side retry is a new seal and a new `jti`. It
recorded that as a residual and left business-key idempotency to the consumer
(`inbox.ProcessOnce` over a `messaging.WireDedupKey` of a signed business id).

The motivating producer, described by shape: a batch producer that persists `(record id, jti,
sealed bytes)` before publishing and must fail loudly on an unroutable publish. The outbox
relay cannot serve it. The relay publishes without `Mandatory` (#1819), and it is
asynchronous, so the producer never sees `ErrPublishUnroutable`, a NACK or exhausted retries,
and cannot stop the batch. `Publish` on a `Mandatory` handle fails loudly, but each retry mints
a new `jti` that the consumer's ledger cannot dedup. `Seal` returned no `jti` at all, and the
only read-backs were `jose/sealed.Open` and `OpenDocument`, which both decrypted: they needed the
encrypt private key, which the rotation runbook never gives a producer.

## Decision

### 1. `Seal` returns the `jti` (breaking)

`Publisher[T].Seal(ctx, evt T) (data []byte, jti string, err error)`. `jose/sealed.Seal` and
`SealDocument` return the same triple. The seam's `messaging.Sealer.Seal(ctx, evt any)` returns it
too. The `jti` is the bare signed slot, `""` on every error; the go-bricks inbox stores
`<SignFamily>:<jti>`. Nothing else about `Seal` changes: it publishes nothing, the outbox lane
persists its bytes as-is and the relay moves them byte-identical (ADR-097), and a plain `T`
returns `ErrNotSealTagged`. There is no additive `SealWithID` and no exported unverified peek:
the only read-back is the verified one (§2), as #1409 kept `open-event` from peeking wire kids. The
`Sealer` method set changes rather than gaining an optional interface. An optional
`IdentifiedSealer` the handle cannot do without must either fail every seal-tagged declaration
when absent, which moves a method-set change from compile time to startup, or fail `Seal` at
call time. Both rank below a compile error. The only external implementer would be a codec
registered through the exported `messaging.RegisterSealCodec` (#1872).

### 2. `jose/sealed.Verify`

`Verify(body []byte, spec *Spec, opts *OpenOptions) (*Envelope, error)` is split from the pipeline
`Open` and `OpenDocument` share, just before rule 10 resolves the encrypt key. It runs rules 1–9
unchanged. It runs rule 10 up to the encrypt-family pin, then resolves the inner `kid` through
`opts.Keys.PublicKey` where `Open` resolves the private key, and stops. It uses no private key,
does no decrypt and skips rule 11. `spec` may be a scanned or a document `Spec`, and `opts.Keys`
is asked for PUBLIC keys only, so a producer can run it. Every refusal is code-identical to
`Open`'s (a vector test pins all published vectors). What `Verify` accepts and `Open` refuses is
any Subject that does not decrypt under the named key (the wrong key under the right `kid`, or a
corrupt encrypted key, IV, ciphertext or tag) and a document that does not decode into the event
type; among the published vectors, exactly `wrong_key_same_name` and
`opened_document_wrong_shape`. Nothing before the decrypt can tell either (see "Residual" below).
`Open` and `OpenDocument` keep their rule order and codes.

### 3. `Publisher[T].PublishSealed` — the door

`func (h *Publisher[T]) PublishSealed(ctx context.Context, client AMQPClient, data []byte) error`,
valid only on a seal-tagged handle. Before any broker I/O it:

1. refuses a plain handle with `ErrNotSealTagged` (its text becomes neutral for `Seal` and
   `PublishSealed`), returns a failed seal setup's startup error as `Publish` does, and returns
   `ErrSealingNotLinked` (wrapped) when the registered codec has no verification side;
2. resolves the tenant exactly as `Publish` does, the context first and then the client's pool
   key, and refuses a disagreement with `ErrTenantStampConflict`;
3. copies `data` once, verifies the copy and publishes the copy, so the bytes on the wire are
   exactly the bytes verified;
4. verifies through the seal-runtime seam: `messaging.SealVerifierProvider`, optional on the
   codec like `messaging.SealOpenerProvider`, is built at declaration and runs
   `jose/sealed.Verify` with the zero `TenantExpectation`. A failure is
   `ErrSealedBytesRejected`, whose chain carries a `*messaging.SealOpenRefusedError` (the code
   without a jose import) whose cause is the
   `*jose/sealed.OpenError`;
5. applies the `tid` rule: the verified `tid` (absent counts as `""`) must equal the resolved
   tenant, or the publish fails with `ErrSealedTenantMismatch`. Bytes sealed without a tenant
   cannot go through a per-tenant client, and bytes sealed for tenant A are never stamped B. The
   producer's store keeps the tenant and restores it into `ctx`.

It then publishes through the same internal door as `Publish`: the handle's exchange, routing key
and declared headers, `Mandatory`, `application/jose`, `type` = the event type, and no preset
`message_id`. Tenant stamping, trace injection, bounded retries, confirms, returns (ADR-122) and
redeclare-on-reconnect are therefore shared code. The `message_id` is minted per call, never
derived from the `jti`, because ADR-122 matches a return by its unique in-flight id. After
verification every error is the client's own, returned as `Publish` returns it.

Wire kids resolve by entry name with no activation filter. Stored `v<N>` bytes keep verifying
after `messaging.seal.active` flips to `v<N+1>`, until rotation step 5 removes `v<N>` from the
producer's keystore. The producer role-tags every provisioned RSA generation of both families
(`keystore.RoleTagSeal`) at declaration, as the opener does. `SealedEventPublisher[T]` (`Seal` +
`PublishSealed`) is the injection seam for a module that persists sealed bytes; `*Publisher[T]`
satisfies it, and `EventPublisher[T]` is unchanged, so consumer-written fakes keep compiling.
`messaging/testing.CaptureSealedPublisher[T]` satisfies it too: its `Seal` records the event and
mints placeholder bytes and a `jti`, its `PublishSealed` records a copy of every body, and it
never seals, verifies or reaches a broker.

### 4. Amendments

- ADR-096: a second, narrow exported path to the wire, valid only on a seal-tagged handle and
  only for bytes that verify against that handle's own declaration.
- ADR-097: its Consequences residual "A caller-side retry after exhausted in-loop retries is a
  new seal and a new `jti`" is withdrawn for producers that persist sealed bytes.
  Producer-owned sealed-bytes stores join the rotation drain gate, and sign-family step 5
  removes `v<N>` from the producer's keystore as well as every consumer's (both in
  [sealing.md's rotation runbooks](sealing.md#rotation-runbooks)).

## Threat model

- **Forged header over plaintext.** Checks limited to the unauthenticated protected header
  would pass a forged header over a plaintext Subject and put plaintext on the broker. The door
  verifies the signature, the slots, the manifest, the inner JWE header, `iss`, the encrypt
  family and both keys before any broker I/O. A body that is not a sealed JWS is `NOT_SEALED`.
- **Retired generation.** The door admits exactly what the producer's keystore holds. Step 5
  removes the entry, and destroying only the private key is not enough: a public-only entry
  still resolves, and the door would admit bytes every consumer refuses. Once step 5 has run,
  the door refuses the stored row with `SEAL_KID_UNKNOWN_GENERATION`, and
  `SealOpenRefusedError.Recoverable` is true as on the consumer, because it names the
  key-provisioning class. The producer still treats a retired generation as final:
  re-provisioning `v<N>` on the producer alone would admit bytes every consumer refuses, so
  recovery is a fresh `Seal` with a new `jti`.
- **Cross-tenant replay.** Strict `tid` equality means the signed tenant and the stamp cannot
  diverge.
- **Ciphertext at rest.** A sealed-bytes store is storage. CVV/CVC, full track data and PIN
  blocks stay out of any sealed event whose bytes are persisted. Persisted ciphertext stays
  readable until every retired encrypt private key that sealed it is destroyed (no forward
  secrecy).
- **Exported codec registration (#1872).** A module that registers its own codec can supply a
  verifier that admits anything. The door trusts the registered codec as every sealed door
  already does, and #1872 tracks unexporting the hook.
- **Residual.** `Verify` does not examine the JWE encrypted key, IV, ciphertext or tag. A body
  signed by the producer's own sign family whose Subject does not decrypt under the named key
  (the wrong key under the right `kid`, or a corrupt encrypted key, IV, ciphertext or tag), or
  whose document does not decode into `T`, therefore passes the door. The consumer refuses it
  (`SEAL_DECRYPT_FAILED`, `SEAL_PAYLOAD_UNDECODABLE`) into the DLQ. Only a holder of the
  producer's sign private key can mint one, and the residual is accepted.

## Alternatives considered

- **Header-only checks.** Rejected: see "Forged header over plaintext".
- **Additive `SealWithID`, or an exported unverified `jti` peek.** Rejected: two ways to seal, and
  an unauthenticated read-back of the same kind #1409 refused.
- **A caller-supplied or derived `jti` on `Publish`.** Rejected: the `jti` stays minted by the
  sealer, and nothing the caller passes chooses it.
- **`Mandatory` on outbox relay rows (#1819).** Out of scope. It would still give the producer
  no synchronous feedback.
- **An optional `IdentifiedSealer`.** Rejected: see §1.
- **Failing startup when the codec has no verification side.** Rejected: a service that never
  calls `PublishSealed` must start. The error is the handle's, returned by `PublishSealed` alone.
- **`message_id` derived from the `jti`.** Rejected: concurrent republishes of one body would
  share the id ADR-122 matches returns by.

## Consequences

- Breaking at compile time (`fix(messaging)!:`): see [migrations.md](migrations.md) `[C70.15]`.
- A republish is deduplicated by the consumer only within `inbox.retentionperiod` (7 days by
  default), and only while the bytes' sign and encrypt generations are still provisioned there.
  After step 5 the producer refuses them, and recovery is a fresh `Seal` with a new `jti`: the
  one residual kept.
- Neither this door nor `Mandatory` signals queue capacity. A queue length limit with
  `x-overflow: reject-publish` (or `reject-publish-dlx`) makes the broker NACK, and the caller
  sees `ErrPublishNacked`. Relay rows for an exchange this service declares can be captured with
  an `alternate-exchange`.
- A verification costs one RSA signature check and header parsing per `PublishSealed`. It does
  no decrypt and emits no metric.
- #1496 (deferred confirmations): one rewrite of the shared door covers both methods.

## References

- Issues: #1869 (this decision), #1307, #1347, #1350, #1409, #1542, #1794 (ADR-122), #1819, #1362, #1856, #1872, #1873
- [ADR-096](adr_096_typed_publish_door.md), [ADR-097](adr_097_sealed_amqp_messages.md),
  [ADR-105](adr_105_framework_writes_every_publish_property.md),
  [ADR-122](adr_122_returned_mandatory_publish_fails.md)
