# Sealed AMQP Messages

Field-level payload protection for events that cross a broker: one declared **Subject**
field travels encrypted, every sibling field stays readable, and the whole document is
signed by the producer. Decision record: [ADR-097](adr_097_sealed_amqp_messages.md).
Vocabulary is the `Payload sealing` section of `CONTEXT.md`; this page uses it without
redefining it.

Packages: `jose/sealed` (codec — `ScanType`, `Seal`, `Open`, `Verify`, the failure codes) and
`messaging/sealed` (the adapter that engages the codec from the typed publish and consume
doors; import-gated like `messaging/streams`, ADR-091). The gate keeps the `messaging`
package free of go-jose and turns a forgotten import into a loud startup error; it does not
make an app smaller, since HTTP JOSE already links go-jose.

How the adapter is wired: `messaging/sealed`'s `init` calls `messaging.RegisterSealCodec`
with the codec (the seam lives in `messaging/internal/sealruntime`); `app` calls
`messaging.ConfigureSealing` with a `messaging.SealRuntime` (key store, the
`messaging.seal.active` selector, tenancy, meter) before any `DeclareMessaging`, and modules
reach it through `messaging.SealingRuntime()`. `Declarations.Validate` fails a seal-tagged
declaration with `messaging.ErrSealingNotLinked` ("import messaging/sealed"),
`ErrNotConfigured` or `ErrKeyStoreMissing`. `messaging.IsSealTagged` (tag key
`messaging.SealTagName`) is the one predicate every door asks; the lane guards use it to
refuse a seal-tagged `T` on streams and on the outbox struct door. `Publisher[T].Publish`
seals when `T` is seal-tagged; `Publisher[T].Seal(ctx, evt)` runs the same sealer once and
returns the body `Publish` would have put on the wire, for the outbox lane or a producer-owned
store that republishes it through `Publisher[T].PublishSealed`, verified through the codec's
optional `messaging.SealVerifierProvider` ([ADR-131](adr_131_sealed_bytes_publish_door.md));
the consumer side opens through the codec's `messaging.SealOpenerProvider` (#1359). Metrics:
`seal.operation.duration` with `seal.operation = seal|open`, and
`seal.open.failures.total` with `seal.error.code`.

## Threat model

Two properties at once, from one declaration both sides share:

- **Confidentiality from the broker and every non-audience reader.** Ops, tooling and other
  tenants' consumers can read queue contents; the Subject's plaintext must not be among
  them. Clear routing fields (order id, amount, event type) stay readable for routing, DLQ
  triage and the broker UI.
- **Producer authenticity for the consumer.** An AMQP publish ACL says who may write to an
  exchange, not who wrote a given message; `x-outbox-event-id` is a rewritable header. The
  consumer needs a signature it can pin to a known producer family.

Ordering is the security decision: **encrypt the Subject first, sign the whole result**. A
signature over a plaintext PAN would be a confirmation oracle — a known BIN plus Luhn makes
guesses enumerable and the public verify key confirms each one — so the signature covers
ciphertext, never a plaintext sensitive field. HTTP jose parity is algorithmic (same
allowlist), not ordering.

What sealing does NOT do: it never judges replay, duplicates or freshness. Those are the
consumer's ledger (`inbox.ProcessOnce`), see [Replay stance](#replay-stance).

Fields you seal likely belong in `log.sensitivefields` too — the logger's masking
vocabulary is independent of the `seal` tag ([observability.md](observability.md#sensitive-data-filtering)).

## Envelope

`delivery.Body` is ONE RFC 7515 compact JWS. Its payload is the business JSON with the
Subject member replaced **in place** by an RFC 7516 compact JWE string. Signed bytes are
wire bytes: no canonicalization, and (G9) wire member order equals struct order, clear
fields never pass through a `map`, and the Subject member is always present (nil Subject
seals as a JWE of `null`).

| Layer | Header params | Notes |
| --- | --- | --- |
| Outer JWS | `typ: vnd.gobricks.sealed.v1+json` · `cty: application/json` · `alg` (PS256 produced; RS256 accepted) · `kid` = concrete sign Generation · `sp` · `jti` · `iat` · `etyp` · `tid` | `typ` is the only sealed-message marker; there is no `x-sealed` AMQP header. `sp` is the signed sealed-paths manifest — one path in v1, constant per event type. |
| Inner JWE | `alg: RSA-OAEP-256` · `enc: A256GCM` · `kid` = concrete encrypt Generation · `cty: application/json` · `iss` = the outer `kid` | `iss == kid` is the authorship binding that kills strip-and-re-sign; no stock JOSE library checks it, so the contract carries it as a MUST with a negative vector (`jose/sealed/testdata/vectors.json`). |

Slots (all signed; `jti`, `iat` and `etyp` always present, `tid` present exactly when the
producer carried a tenant stamp — its presence rule is the tenancy rule under
[`tid` by tenancy](#tid-by-tenancy)):

| Slot | Written by the sealer as | Judged by the opener as |
| --- | --- | --- |
| `jti` | a fresh UUID per seal, byte-stable across every redelivery | presence + the header-id grammar; never looked up |
| `iat` | seal time (integer NumericDate) | present, integer, non-negative — informational, never compared to a clock |
| `etyp` | the publisher declaration's `EventType` | must equal the consumer declaration's `EventType` |
| `tid` | the ADR-087 tenant stamp when non-empty, omitted otherwise | by tenancy — see [tenancy](#tid-by-tenancy) |

AMQP `ContentType` is `application/jose` on a typed-door publish (`Publish`,
`PublishSealed`); a persisted-sealed outbox row relays as `application/octet-stream`, since a
`[]byte` payload carries no content type
([ADR-105](adr_105_framework_writes_every_publish_property.md), #1873). Wire floor is ≈1.4 KB
per message (RSA-wrapped CEK + PS256, base64): the prototype measured 104 B → 1505 B, and a nil
Subject 65 B → 1435 B. Ciphertext length grows with the plaintext, so a broker reader learns the
Subject's size class.

## Tags

The `seal` tag family is distinct from `jose` so one struct can never be both an HTTP body
and a sealed event by accident. A declaration is a sentinel plus exactly one Subject:

```go
type PaymentAuthorized struct {
    _        struct{} `seal:"sign=svc-payments-sign,encrypt=aud-core-encrypt"`
    OrderID  string   `json:"order_id"  validate:"required"`
    Amount   int64    `json:"amount"    validate:"gt=0"`
    Card     Card     `json:"card"      seal:"subject"` // its json name is the sp entry
}
```

- `sign=<logical>` and `encrypt=<logical>` are **Logical kids**, never generations: rotation
  never edits a tag. Both sides carry the same two names and derive their own role
  (producer: sign PRIVATE + encrypt PUBLIC; consumer: sign PUBLIC + encrypt PRIVATE).
- Scan errors (`jose/sealed.ScanType`, `SEAL_TAG_*` codes, startup-fatal): a malformed
  sentinel, a kid failing the Logical grammar, zero or several Subjects, a Subject without a
  sentinel, or a Subject that could vanish from the wire — embedded, unexported, `json:"-"`,
  `omitempty`, `omitzero`.
- A seal-tagged `T` requires the `WithMeta` consume door (`DeclareTypedConsumerWithMeta`) so
  the Dedup key is reachable; the meta-less door refuses it at startup (#1359). Streams typed
  declarations refuse a seal-tagged `T` in v1 (#1360). `outbox.Publish` refuses a seal-tagged struct
  payload with `outbox.ErrSealedPayloadNeedsBytes` (#1360). The outbox flow is
  `bytes, jti, err := h.Seal(ctx, evt)` inside the business transaction, then
  `deps.Outbox.Publish(ctx, tx, event)` with those bytes as the payload: the record keeps
  that one seal result and the relay republishes it byte-identical on every drive, so the
  `jti` is stable across redeliveries; a second `Seal` call is a new seal and a new `jti`.
  The returned `jti` is the bare signed slot (`""` on error), held by the caller of `Seal`
  (the producer): a producer keying its own ledger on it uses it as is, while a consuming
  app gets the namespaced `Meta.DedupKey()`, `<SignFamily>:<jti>`, which the go-bricks
  inbox stores ([ADR-131](adr_131_sealed_bytes_publish_door.md)).
  `Seal` on a plain `T` is `messaging.ErrNotSealTagged` (#1358).

## Keys

| Term | Where it lives | Rule |
| --- | --- | --- |
| Logical kid | the tag | jose kid alphabet `^[A-Za-z0-9_-]+$`, ≤64 chars (`sealed.MaxLogicalKidLen`), narrowed to `^[a-z0-9-]+$` by the env-reachability rule (ADR-090), never ending in `-v<digits>` |
| Generation | keystore entry `<logical>-v<N>` | `N` a positive integer without leading zeros (`v1`, not `v0`/`v01`); ordering is integer comparison; `keystore.Generation.Kid()` is the wire kid |
| Accept set | the consumer's keystore | exactly the provisioned generations of the family in the inherited role (`keystore.FamilyEnumerator`); provisioning is the sole trust act — no accept-list config exists |
| Activation | `messaging.seal.active.<logical>: v<N>` on the producer | resolved by `keystore.ActiveGeneration` at startup for every Logical kid the producer resolves, sign and encrypt alike: one generation auto-activates, several with no selector refuse startup, a selector naming an unprovisioned generation refuses startup |
| Family pin | the opener | the wire `kid` must be a Generation of the declared family (`SEAL_KID_FAMILY_MISMATCH`) AND resolve locally (`SEAL_KID_UNKNOWN_GENERATION`, recoverable) |

Granularity: one sign family per producing service, one encrypt family per audience.
Per-queue keys are forbidden; per-tenant keys are forbidden in v1 (a tag is a compile-time
constant, and shared-mode producers hold every key anyway). Distribution is out-of-band —
no JWKS. Never reuse an entry name between HTTP jose and sealing: the keystore records
which role tag resolved each entry (`keystore.RoleTagJoseRoute` from the server's jose
wiring, `keystore.RoleTagSeal` from `messaging/sealed`) and WARNs at startup, naming the
entry, when one entry serves both.

The env door for the selector is narrower than YAML: `MESSAGING_SEAL_ACTIVE_<KID>` reaches a
kid spelled in `[a-z0-9]` everywhere, a hyphenated kid only where the runtime allows `-` in
a variable name (Docker and Kubernetes manifests yes, POSIX `export` no), otherwise the
selector is YAML-only ([keystore.md](keystore.md#activation-messagingsealactive)).

```yaml
keystore:
  keys:
    svc-payments-sign-v1: { private: { file: certs/payments-sign-v1.der } }   # producer
    aud-core-encrypt-v1:  { public:  { file: certs/core-encrypt-v1.der } }    # producer
messaging:
  seal:
    active:
      svc-payments-sign: v1
      aud-core-encrypt: v1
```

## Rotation runbooks

Every step requires ordering, never simultaneity; both sides keep verifying and decrypting
throughout because each message names the generation that sealed it. The drain gate is the
same for both families: **queue depth AND the outbox retention window AND DLQ replay policy
AND inbox parks AND every producer-owned sealed-bytes store** — old-generation rows replay
byte-identical for the full retention window, and stored sealed bytes republish through
`PublishSealed` until step 5 removes their generation from the producer, so gating on queue
depth alone strands them unopenable. The consumers-before-flip gate is
human-enforced until #769; getting it wrong shows up as a DLQ spike of
`SEAL_KID_UNKNOWN_GENERATION`.

### Sign family (`sign=<logical>`)

1. Provision `<logical>-v<N+1>` **PUBLIC** to every consumer — the accept set widens; harmless,
   no such traffic yet.
2. Provision the `v<N+1>` **PRIVATE** to the producer — inert, `v<N>` is still active.
3. Flip `messaging.seal.active.<logical>: v<N+1>` on the producer and redeploy. New traffic
   seals under `v<N+1>`; in-flight and outbox-replayed `v<N>` traffic still opens per message.
4. Drain gate (above).
5. Remove the `v<N>` entries from every consumer AND from the producer's keystore (the accept
   set and the producer's sealed-bytes door both shrink); destroy the retired private.
   Destroying the private alone is not enough: a public-only `v<N>` entry on the producer still
   resolves, so `PublishSealed` would keep admitting `v<N>` bytes every consumer now refuses.

### Encrypt family (`encrypt=<logical>`)

The roles invert, so the order does too (G3):

1. Provision `<logical>-v<N+1>` **PRIVATE** to every consumer first — they can decrypt
   `v<N+1>` before any exists.
2. Provision the `v<N+1>` **PUBLIC** to the producer.
3. Flip `messaging.seal.active.<logical>: v<N+1>` on the producer and redeploy.
4. Drain gate (above).
5. Remove `v<N>` from producer and consumers — on the producer that also stops `PublishSealed`
   admitting `v<N>` bytes; destroy the retired privates — until the last one is gone, captured
   and persisted ciphertext stays readable (no forward secrecy, no revocation).

### Provisioning a consumer N+1

A new audience member for an already-sealed event type needs, before its first delivery:
the sign family's currently accepted generations as **PUBLIC** entries (every generation
still in flight, not only the active one), the encrypt family's accepted generations as
**PRIVATE** entries, the same two Logical kids in its tag, a `WithMeta` consumer declaring
the producer's `EventType`, and an inbox ledger. Nothing changes on the producer: the
encrypt family is per audience, so a new member of the same audience shares the key; a new
audience is a new encrypt family and a new sealed event type.

## Opening: rule order

The opener (`jose/sealed.Open`) applies the v1 rules in order; the first failing rule wins
and names itself through the code. Rules 1–4 run on the peeked, still unauthenticated
protected header, before any signature parsing; nothing in rules 1–9 touches the inner JWE;
no clock is read.

| # | Rule | Code |
| --- | --- | --- |
| 1 | body is a compact JWS whose `typ` is `vnd.gobricks.sealed.v1+json` | `NOT_SEALED` |
| 2 | `alg` ∈ {PS256, RS256}; `cty: application/json`; no `crit`; unknown params ignored | `SEAL_ALG_NOT_ALLOWED` / `SEAL_CTY_INVALID` / `SEAL_CRIT_PRESENT` |
| 3 | `kid` is a Generation of the declared sign family | `SEAL_KID_FAMILY_MISMATCH` |
| 4 | `kid` resolves to a PUBLIC key in the local keystore | `SEAL_KID_UNKNOWN_GENERATION` (recoverable — the rotation-lag signature) |
| 5 | signature verifies over the exact payload bytes | `SEAL_SIGNATURE_INVALID` |
| 6 | `jti` / `iat` / `etyp` / `sp` present and well-formed | `SEAL_HEADER_SLOT_INVALID` (detail `slot`: presence and length only) |
| 7 | `etyp` equals the declared `EventType` | `SEAL_EVENT_TYPE_MISMATCH` |
| 8 | `tid` satisfies the tenancy expectation | `SEAL_TENANT_MISMATCH` |
| 9 | `sp` equals the declared sealed set | `SEAL_MANIFEST_MISMATCH` |
| 10 | payload is an object, the Subject member is a compact JWE, inner header passes rule 2 (detail `layer: jwe`), `iss` equals the outer `kid`, inner `kid` is a Generation of the encrypt family that resolves to a PRIVATE key, decrypt | `SEAL_PAYLOAD_UNDECODABLE` / the rule-2–4 codes with `layer: jwe` / `SEAL_AUTHORSHIP_MISMATCH` / `SEAL_DECRYPT_FAILED` |
| 11 | splice the plaintext back and unmarshal into the event type | `SEAL_PAYLOAD_UNDECODABLE` |
| 12 | build the `Envelope` | — |

Producer side: `jose/sealed.Verify` runs rules 1–9 and rule 10 up to the encrypt-family pin,
resolving the inner `kid` as a PUBLIC key, and stops before the decrypt; every refusal carries
`Open`'s code. The sealed-bytes door below runs it.

Wiring mistakes (no `Spec`, no `KeyResolver`, empty `EventType`, wrong `out` type) are
`SEAL_OPTIONS_INVALID` / `SEAL_TYPE_MISMATCH` as rule 0 — the same error type, never a
per-message poison class. The sealer's own codes are `SEAL_TAG_INVALID`,
`SEAL_TAG_KID_INVALID`, `SEAL_TAG_SENTINEL_MISSING`, `SEAL_TAG_SUBJECT_MISSING`,
`SEAL_TAG_SUBJECT_MULTIPLE`, `SEAL_TAG_SUBJECT_INVALID`, `SEAL_KID_FAMILY_MISMATCH`,
`SEAL_OPTIONS_INVALID`, `SEAL_TYPE_MISMATCH`, `SEAL_DOCUMENT_INVALID`, `SEAL_FAILED`
(`jose/sealed/errors.go`).

Every failure is one `*sealed.OpenError`: `errors.Is` reaches the sentinel
(`ErrNotSealed` vs `ErrOpenFailed`), `Code` names the rule, and details carry presence,
length and layer only — a signed value is not a log-safe value (ADR-081). On the consume
door an open failure is a nack without requeue into the standard DLQ path as a
`*messaging.PayloadError` at payloaderr stage `open` (`messaging.PayloadStageOpen`,
sentinel `messaging.ErrPayloadOpenRefused` — match with `errors.Is`), so ops can tell a
signature-invalid spike from JSON garbage; the `*sealruntime.OpenRefusedError` wrapping the
`*sealed.OpenError` stays in the chain (#1359).

### `tid` by tenancy

| Tenancy | Rule |
| --- | --- |
| `messaging.tenancy: shared` | a signed `tid` is REQUIRED (absent is poison) and equality-checked against the carrier's tenant; a consumer declaring `TenantOptional` accepts absent, and a present `tid` is equality-checked whenever the carrier carries a tenant (G10) |
| shared, `TenantOptional`, delivery unstamped, signed `tid` present | accepted; the `tid` is surfaced on `Meta.Sealed().TenantID` and not compared (an optional consumer accepts unstamped deliveries by declaration; refuse in the handler on `env.TenantID` if that matters) |
| per-tenant | present-and-different from the context tenant is poison; absent is accepted |
| `multitenant.enabled: false` | no rule; the value is surfaced on the envelope (G2) |

`tid` upgrades tenant routing from producer-claimed (a rewritable header) to
producer-signed; the ACL remains the authorization boundary.

## Replay stance

Redelivery (a crash before ack, a DLQ drain, a shovel, an outbox row driven again) and
replay (an attacker re-injecting a captured message) are byte-identical, so no cryptography
tells them apart. The seal layer therefore performs **no replay, duplicate or freshness
rejection**; its one replay-related job is to make the message's identity un-forgeable.

- `Meta.Sealed() (SealedEnvelope, bool)` — true for every delivery a seal-tagged `T`
  receives, false for every delivery a plain typed consumer receives: a property of the
  consumer TYPE, so a handler branching on it cannot be steered by a header.
- `Meta.DedupKey() (messaging.DedupKey, error)` — a value carrying which door produced it.
  For a seal-tagged `T` it is a SEALED key spelling `<SignFamily>:<jti>` (never errors; the
  Logical family, not the Generation, so a rotation does not re-open the window); for a
  plain `T` it is a WIRE key holding the `x-outbox-event-id` header once it passes
  `^[A-Za-z0-9_-]{1,128}$` — or, when the delivery carries no such header, the AMQP
  `message_id` property under that same grammar — or an error wrapping
  `messaging.ErrInvalidEventID`. `key.Sealed()` reports the provenance and `key.String()`
  the persisted spelling; the zero value is invalid.
- **Only the sealed branch of `Metadata.DedupKey` can mint a sealed key.** No exported door
  does: `messaging.WireDedupKey`, the one constructor for a wire-sourced or
  consumer-composed id, returns an unsealed key whatever the id spells. So admission at the
  ledger is by TYPE and provenance, not by spelling.
- `inbox.ProcessOnce` (through `messaging.ValidateDedupKey`) refuses the zero key, and
  refuses a SEALED key unless the context carries a sealed delivery key that equals the
  one being validated (`messaging.IsSealedDelivery`). The marker is that delivery's own
  `DedupKey`, so what it catches is both a context that never came from the sealed door
  and a key whose `<SignFamily>:<jti>` differs from the bound one — delivery IDENTITY is
  never compared, so a redelivery of the same envelope composes the same key and passes,
  while a sealed key whose value differs from the bound key does not: `context.Background()` drops it
  and fails closed with `ErrInvalidEventID` instead of writing the ledger row silently,
  and a handler that carries delivery A's key into B's context is refused the same way.
  `context.WithoutCancel(ctx)` keeps every value, the bound key included, so detached work
  derived that way still passes admission for THAT delivery's key — which is the point:
  give it a fresh bounded timeout rather than reaching for `Background`.
- The header-id grammar excludes `:`, so no header-sourced or consumer-composed id can even
  spell a sealed key: a publish-ACL holder on an unsealed sibling queue cannot pre-insert a
  sealed message's key and have the real one skip+ACK (the shared-ledger suppression
  attack). That grammar applies to unsealed consumers too — [migrations.md](migrations.md)
  `[C63.2]`. The typed key makes that a belt-and-braces second line rather than the
  boundary itself.
- `inbox.retentionperiod` **is** the replay window: a capture-then-wait replay older than
  retention re-executes if its Generation is still accepted. Retention must exceed the
  broker's redelivery window AND cover the DLQ drains and outbox re-drives you intend to
  replay. The ledger's duplicate short-circuit emits a counter and a log line — the only
  observable of a replay campaign.
- `etyp` closes the one class a ledger cannot: cross-type reroute (a captured
  `card.tokenized` fed to the `card.deleted` consumer, verifying under the same producer
  key, whose ledger has never seen that `jti`). Consequences: one sealed event type per
  queue; a DLQ watcher declares the producer's `EventType` or uses a raw handler; an
  `EventType` rename is a coordinated release.
- A caller-side retry after `Publish` exhausts its in-loop retries is a new seal and a new
  `jti`; business-key idempotency stays the consumer's contract — unless the producer persisted
  `Seal`'s bytes and republishes them through `PublishSealed`, which keeps the `jti` (ADR-131).
  A stateless sealed consumer (no ledger) leaves every replay class open — the `WithMeta`
  requirement is the nudge.

## Republishing stored sealed bytes (`PublishSealed`)

A producer that persists `(record id, jti, sealed bytes)` before publishing, and must fail
loudly when a publish is unroutable, republishes those exact bytes through the handle that
sealed them ([ADR-131](adr_131_sealed_bytes_publish_door.md)). Declare that handle
`Mandatory: true`.

```go
data, jti, err := h.Seal(ctx, evt) // persist data, jti and the tenant with the record
// later, in the batch — restore the stored tenant first:
ctx = multitenant.SetTenant(ctx, rec.Tenant)
err = h.PublishSealed(ctx, client, data)
```

`PublishSealed` exists only on a seal-tagged handle and refuses before any broker I/O:

| Refusal | Error |
| --- | --- |
| plain `T` | `messaging.ErrNotSealTagged` |
| the handle's seal setup failed at declaration | that startup error (for example `messaging.ErrSealingNotLinked`) |
| the registered codec has no producer verification | `messaging.ErrSealingNotLinked`, wrapped; `Publish` and `Seal` still work |
| the context's tenant disagrees with the client's pool key | `messaging.ErrTenantStampConflict`, as `Publish` |
| the bytes fail verification | `messaging.ErrSealedBytesRejected`; the chain carries a `*messaging.SealOpenRefusedError` whose `Code` is the rule's `SEAL_*` code, and `errors.As` reaches the `*jose/sealed.OpenError` |
| the signed `tid` is not the tenant this publish would stamp | `messaging.ErrSealedTenantMismatch` |

**Checks.** Verification runs the opener's rules 1–9 unchanged:

- a compact JWS with `typ` `vnd.gobricks.sealed.v1+json`;
- the outer `alg`/`cty`/`crit` policy;
- a sign `kid` that is a Generation of the declared sign family and resolves to a PUBLIC key;
- the signature;
- well-formed `jti`/`iat`/`etyp`/`sp` slots;
- `etyp` equal to the handle's `EventType`;
- `sp` equal to the declared sealed set.

It then runs rule 10 up to the decrypt: the Subject is a compact JWE whose header passes the
inner policy, whose `iss` equals the outer `kid`, and whose `kid` is a Generation of the declared
encrypt family that resolves to a PUBLIC key. It resolves no private key, decrypts nothing and
decodes nothing. Kids resolve by entry name, with no activation filter. Bytes sealed under
`v<N>` keep publishing after `messaging.seal.active` flips to `v<N+1>`, until step 5 removes
`v<N>` from this producer's keystore.

**Tenant rule.** The signed `tid` must equal the tenant `Publish` would stamp for the same `ctx`
and client: the context's tenant, else the client's pool key. An absent `tid` counts as no
tenant. So bytes sealed without a tenant cannot go through a per-tenant client, and bytes sealed
for tenant A are never stamped B. Store the tenant with the bytes and restore it into `ctx`
before republishing.

**Publishing.** The door publishes the one copy of `data` it verified, so a buffer the caller
mutates mid-call cannot change the wire. It uses the same internal door as `Publish`: the
handle's exchange, routing key and declared headers, `Mandatory`,
`content_type: application/jose`, `type` = the `EventType`, the tenant stamp, trace headers,
bounded retries, confirms and redeclare-on-reconnect. The AMQP `message_id` is minted per call
and never derived from the `jti`, because ADR-122 matches a returned publish by it.

**Errors after verification** are the client's own, exactly as `Publish` returns them. On a
`Mandatory` handle whose routing key reaches no queue, that is `ErrPublishUnroutable` under
`ErrPublishRetriesExhausted`. When a deadline, a cancel or a shutdown cuts the retries short, it
is joined with `context.DeadlineExceeded`, `context.Canceled` or `ErrShutdown` instead, so match
`ErrPublishUnroutable` with `errors.Is`. Neither the door nor `Mandatory` signals queue capacity.
A queue length limit with `x-overflow: reject-publish` (or `reject-publish-dlx`) makes the
broker NACK, and the caller sees `ErrPublishNacked`.

**Dedup.** Every republish of the same bytes carries the same `jti`. `Seal` returns it bare, and
the go-bricks inbox stores `<SignFamily>:<jti>`. A retry is therefore deduplicated only within
`inbox.retentionperiod` (7 days by default), and only while the bytes' sign and encrypt
generations are still provisioned on the consumer. After step 5 the producer refuses the stored
bytes, and recovery is a fresh `Seal`, which mints a new `jti`.

**Residual.** A body signed by this producer's own sign family but encrypted to the wrong key
under the right `kid`, or whose document does not decode into `T`, passes the door. The consumer
refuses it (`SEAL_DECRYPT_FAILED`, `SEAL_PAYLOAD_UNDECODABLE`) into the DLQ.

**At rest.** A sealed-bytes store is storage, whatever the encryption. Keep CVV/CVC, full track
data and PIN blocks out of any sealed event whose bytes are persisted, the same rule as the
outbox's SAD warning ([outbox.md](outbox.md)). Persisted ciphertext stays readable until every
retired encrypt private key that sealed it is destroyed (no forward secrecy).

**Tests.** Depend on `messaging.SealedEventPublisher[T]` (`Seal` + `PublishSealed`, which
`*Publisher[T]` satisfies) and inject `messaging/testing.CaptureSealedPublisher[T]`. It mints
placeholder bytes and records every body it is handed.

## Minting test events with rabbitmqadmin (seal-event CLI)

Publishing a sealed event by hand is impractical: the body is a compact JWS whose payload
is the marshaled event with one member replaced by a compact JWE, signed over exactly those
bytes. `cmd/seal-event` mints one from a JSON document using the production
`sealed.SealDocument` path and the keystore's own DER loaders (`internal/keymaterial`), so a
body it emits is one the sealed consume door opens by construction.

Install:

```sh
go install github.com/gaborage/go-bricks/cmd/seal-event@latest
```

Generate DER fixture keys with openssl. The CLI holds the PRODUCER role: the sign PRIVATE
half and the encrypt PUBLIC half. The consumer holds the mirror image — the sign public and
the encrypt private — under the same generation names.

```sh
# Sign pair — the PUBLIC half is what the consumer provisions as
# svc-payments-sign-v1 in its keystore
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -outform DER -out sign.der
openssl pkey -inform DER -in sign.der -pubout -outform DER -out sign.pub.der

# Encrypt pair — the audience's key; the CLI needs only the PKIX DER public half,
# the consumer provisions the private half as aud-core-encrypt-v1
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -outform DER -out enc.der
openssl pkey -inform DER -in enc.der -pubout -outform DER -out enc.pub.der
```

Seal one event body and publish it:

```sh
echo '{"order_id":"o-1","amount":100,"card":{"pan":"4111111111111111","expiry":"12/30"}}' \
  | seal-event \
    -sign-key-file sign.der -encrypt-key-file enc.pub.der \
    -sign-kid svc-payments-sign-v1 -encrypt-kid aud-core-encrypt-v1 \
    -subject card -event-type payment.authorized -tenant-id t1 > body.txt

rabbitmqadmin publish exchange=payments routing_key=payment.authorized \
  payload="$(cat body.txt)" \
  properties='{"content_type":"application/octet-stream","headers":{"x-tenant-id":"t1"}}'
```

Local failures — the CLI exits 1 before signing:

- The Subject named by `-subject` is the JSON member name, and it must be present exactly
  once in the document; an absent Subject, a case-fold twin of it, a non-object document or
  trailing content after it is `SEAL_DOCUMENT_INVALID`.

Consumer-side rejections — the publish succeeds, the open refuses:

- `-tenant-id` writes the signed `tid`; under shared tenancy it must equal the
  `x-tenant-id` header you publish with, or the open fails `SEAL_TENANT_MISMATCH`.
- `-event-type` must equal the consumer declaration's `EventType` — the signed `etyp` is
  compared verbatim (`SEAL_EVENT_TYPE_MISMATCH`).
- Both kids must be provisioned Generations of the tag's families on the consumer:
  `<logical>-v<N>`, never the bare Logical kid. A wrong family is
  `SEAL_KID_FAMILY_MISMATCH`; a right family the consumer has not provisioned is the
  recoverable `SEAL_KID_UNKNOWN_GENERATION`.

PS256 only — there is no `-sig-alg`; the opener also accepts RS256, but the CLI never
emits it.

Each invocation is a fresh seal with a fresh `jti`, so publishing the same `body.txt` twice
is the dedup test and re-running the CLI is not. Go test authors do not need the binary:
mint from a JSON fixture in-process with `sealed.NewDocumentSpec` plus `sealed.SealDocument`,
which is the same path this CLI runs.

## Inspecting sealed events (open-event CLI)

`cmd/open-event` is the mirror of `seal-event`: it verifies and decrypts one sealed body
through the production `sealed.OpenDocument` path and prints what the message proved about
itself. There is no skip-verification mode — it fails exactly where the consume door fails,
with the same `SEAL_*` code.

```sh
go install github.com/gaborage/go-bricks/cmd/open-event@latest

open-event -sign-key-file sign.pub.der -encrypt-key-file enc.der \
  -sign-kid svc-payments-sign-v1 -encrypt-kid aud-core-encrypt-v1 \
  -subject card -event-type payment.authorized \
  -tenancy shared -tenant-id t1 body.txt
```

```text
JTI:        3f2a6c18-7b91-4d0e-9c3a-5e8b1d24af77
IssuedAt:   2026-09-13T09:14:22Z
EventType:  payment.authorized
TenantID:   t1
SignKid:    svc-payments-sign-v1
SignFamily: svc-payments-sign
EncKid:     aud-core-encrypt-v1

{"order_id":"o-1","amount":100,"card":"<redacted>"}
```

It holds the CONSUMER role, so its two key flags are the mirror of `seal-event`'s: the sign
PUBLIC half (`sign.pub.der`, to verify) and the encrypt PRIVATE half (`enc.der`, to
decrypt) — the same two files the openssl recipe above produced.

**The subject is never printed by default.** Its member keeps its place in the document so
the shape stays readable, but its value is the fixed literal `"<redacted>"`: no plaintext,
and no length hint either. `-print-subject` splices the real plaintext instead and writes
one warning line to stderr first — fixture data only, never a production queue's payload.

Both wire kids are required flags, never read from the unauthenticated protected header,
and the Logical family is derived from them the way `seal-event` derives it. A kid that
disagrees with the body is a genuine refusal (`SEAL_KID_FAMILY_MISMATCH`,
`SEAL_KID_UNKNOWN_GENERATION`), not a pre-check.

`-tenancy` names the tid rule to apply. `shared` requires a signed `tid` equal to
`-tenant-id` — the flag stands in for the `x-tenant-id` header the delivery pipeline would
have read. `optional` and `per-tenant` are the SAME rule (`{Expected: tenantID}`: an absent
tid is accepted, a present one that differs is poison) and differ from `shared` only in
whether a tid is required — four mode names, three behaviours; both spellings exist so an
invocation can say which deployment it reproduces. `disabled` — the default — applies no
rule and surfaces whatever tid the wire carries, so passing `-tenant-id` with it is a usage
error (exit 2) rather than a value nobody judges.

Exit codes: `0` opened, `1` tool error (bad or unreadable key, unreadable input), `2` usage,
`3` refused. A refusal prints its code and presence/length details — never a subject byte.
`-json` emits `{"envelope":{…},"document":…}` on success and `{"code":…,"details":{…}}` on
refusal, the latter on stdout so one stream carries the whole result; the rule NUMBER is
omitted from every output, since its numbering is unstable — key on the code. JSON output
keeps Go's default HTML escaping, so on the wire the placeholder is spelled
`"\u003credacted\u003e"` and any JSON decoder reads it back as `<redacted>` — match the
decoded value, never the raw bytes.

`open-event` reads at most 1 MiB from the body file or stdin and refuses a larger input
before anything parses it, so a mistyped path (a log, a core dump) fails at the door instead
of being buffered whole. The cap is this binary's alone — `seal-event` and `seal-payload`
stay uncapped.

## Residuals

- ≈1.4 KB wire floor per message; ciphertext length reveals the Subject's size class.
- No forward secrecy and no revocation channel (static RSA-OAEP): key theft or consumer
  offboarding is a full encrypt-family rotation, and captured ciphertext stays readable until
  the last old private is destroyed. The decrypt private is audience-held.
- The consumers-before-flip gate is human-enforced until #769.
- One producing service per sealed event type; one sealed event type per queue.
- `SEAL_KID_UNKNOWN_GENERATION` fires before verification: unauthenticated and spammable to
  muddy the rotation-lag signal — inherent to kid-before-verify.
- `inbox.retentionperiod` is the replay window; producer and consumer retention live in
  different processes, so only a documented rule and a same-process WARN exist.
- The ledger has no consumer dimension: two consumers in one service on one event collide
  (#1362).
- The header-id grammar is a breaking change for hand-minted ids (`[C63.2]`); the typed
  publish door that sealing engages from removed raw byte publishing (`[C63.1]`, ADR-096).
- A keystore YAML entry binding a name to material remains a trust act, scoped to that
  entry. Two ids per outbox-lane event (`record.ID` and `jti`), correlated by `traceparent`.
- `PublishSealed` admits a body its own sign family signed but encrypted to the wrong key under
  the right `kid`, or whose document does not decode into `T`; the consumer refuses it
  (ADR-131).
- A stored sealed body's `jti` dedups a republish only within `inbox.retentionperiod` and while
  its generations are provisioned on the consumer; after step 5 recovery is a fresh `Seal` and a
  new `jti`.

## Migration pointers

Sealing is greenfield — there is no accept-unsealed mode and no plaintext branch. The two
atoms a sealing adopter meets are ADR-096's `[C63.1]` (publish through
`DeclareTypedPublisher[T]`) and `[C63.2]` (header-sourced event ids must match the grammar),
both in [migrations.md](migrations.md). Module example: `llms.txt`, "Sealed events".
