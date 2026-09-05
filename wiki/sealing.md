# Sealed AMQP Messages

Field-level payload protection for events that cross a broker: one declared **Subject**
field travels encrypted, every sibling field stays readable, and the whole document is
signed by the producer. Decision record: [ADR-097](adr_097_sealed_amqp_messages.md).
Vocabulary is the `Payload sealing` section of `CONTEXT.md`; this page uses it without
redefining it.

Packages: `jose/sealed` (codec — `ScanType`, `Seal`, `Open`, the failure codes) and
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
returns the body `Publish` would have put on the wire, for the outbox lane; the consumer side opens through the codec's `messaging.SealOpenerProvider`
(#1359). Metrics:
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

AMQP `ContentType` stays `application/octet-stream`. Wire floor is ≈1.4 KB per message
(RSA-wrapped CEK + PS256, base64): the prototype measured 104 B → 1505 B, and a nil Subject
65 B → 1435 B. Ciphertext length grows with the plaintext, so a broker reader learns the
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
  `bytes, err := h.Seal(ctx, evt)` inside the business transaction, then
  `deps.Outbox.Publish(ctx, tx, event)` with those bytes as the payload: the record keeps
  that one seal result and the relay republishes it byte-identical on every drive, so the
  `jti` is stable across redeliveries; a second `Seal` call is a new seal and a new `jti`.
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
AND inbox parks** — old-generation rows replay byte-identical for the full retention window,
so gating on queue depth alone strands them unopenable. The consumers-before-flip gate is
human-enforced until #769; getting it wrong shows up as a DLQ spike of
`SEAL_KID_UNKNOWN_GENERATION`.

### Sign family (`sign=<logical>`)

1. Provision `<logical>-v<N+1>` **PUBLIC** to every consumer — the accept set widens; harmless,
   no such traffic yet.
2. Provision the `v<N+1>` **PRIVATE** to the producer — inert, `v<N>` is still active.
3. Flip `messaging.seal.active.<logical>: v<N+1>` on the producer and redeploy. New traffic
   seals under `v<N+1>`; in-flight and outbox-replayed `v<N>` traffic still opens per message.
4. Drain gate (above).
5. Remove the `v<N>` entries from every consumer (accept set shrinks); destroy the retired
   private.

### Encrypt family (`encrypt=<logical>`)

The roles invert, so the order does too (G3):

1. Provision `<logical>-v<N+1>` **PRIVATE** to every consumer first — they can decrypt
   `v<N+1>` before any exists.
2. Provision the `v<N+1>` **PUBLIC** to the producer.
3. Flip `messaging.seal.active.<logical>: v<N+1>` on the producer and redeploy.
4. Drain gate (above).
5. Remove `v<N>` from producer and consumers; destroy the retired privates — until the last
   one is gone, captured ciphertext stays readable (no forward secrecy, no revocation).

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
- `Meta.DedupKey() (string, error)` — `<SignFamily>:<jti>` for a seal-tagged `T` (never
  errors; the Logical family, not the Generation, so a rotation does not re-open the
  window); for a plain `T` the `x-outbox-event-id` header once it passes
  `^[A-Za-z0-9_-]{1,128}$`, or an error wrapping `messaging.ErrInvalidEventID`.
- `Meta.DedupKey()` on a sealed consumer is `<SignFamily>:<jti>`; `inbox.ProcessOnce` admits it
  only under the delivery context the sealed door handed the handler
  (`messaging.IsSealedDelivery`). Call `ProcessOnce` with a context derived from the
  handler's — `context.WithoutCancel(ctx)` for background work, never `context.Background()`
  — or the marker is lost and the call fails closed with `ErrInvalidEventID`.
- `:` is outside the header-id grammar, so no header can spell a sealed key: a publish-ACL
  holder on an unsealed sibling queue cannot pre-insert a sealed message's key and have the
  real one skip+ACK (the shared-ledger suppression attack). That grammar applies to
  unsealed consumers too — [migrations.md](migrations.md) `[C63.2]`.
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
  `jti`; business-key idempotency stays the consumer's contract. A stateless sealed consumer
  (no ledger) leaves every replay class open — the `WithMeta` requirement is the nudge.

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

## Migration pointers

Sealing is greenfield — there is no accept-unsealed mode and no plaintext branch. The two
atoms a sealing adopter meets are ADR-096's `[C63.1]` (publish through
`DeclareTypedPublisher[T]`) and `[C63.2]` (header-sourced event ids must match the grammar),
both in [migrations.md](migrations.md). Module example: `llms.txt`, "Sealed events".
