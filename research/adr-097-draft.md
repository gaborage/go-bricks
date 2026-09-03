# ADR-097: Sealed AMQP Messages — Field-Level JOSE Payload Protection

- **Status**: Proposed (ships with the last link of the #1357 stack, #1361)
- **Date**: 2026-09-03
- **Related**: [ADR-096](adr_096_typed_publish_door.md) (the typed door sealing
  engages from), [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md) (the tenant
  stamp mirrored into the signed `tid`), [ADR-091](adr_091_streams_opt_in_registration.md)
  (the import-gate pattern `messaging/sealed` follows), [ADR-081](adr_081_panic_type_only.md)
  / ADR-070 (why `etyp`/`jti`/`tid` values are never logged), the HTTP JOSE ADRs
  (algorithm allowlist parity)
- **Issue**: #1301 (map); decisions #1304 (envelope), #1305 (tags, package, publish
  break), #1306 (keys), #1307 (replay); #1308 (prototype); #1309 (spec-gap decisions,
  2026-09-03). Research branches `research/amqp-envelope-standards`,
  `research/amqp-seal-seams`; prototype `prototype/amqp-seal-open`.

## Context

Payment events cross a broker that ops, tooling and other tenants' consumers can
read. Two properties are needed at once: confidentiality of the card subtree from the
broker and from every non-audience reader, and producer authenticity for the
consumer — an AMQP publish ACL says who may write to an exchange, not who wrote a
given message, and `x-outbox-event-id` is a rewritable header. Clear routing fields
(order id, amount, event type) must stay readable for routing, DLQ triage and the
broker UI. A cleartext signature over a plaintext PAN would be a confirmation oracle
(known BIN + Luhn make guesses enumerable; the public verify key confirms them), so
the signature must cover ciphertext, never plaintext sensitive fields.

Sealing is greenfield: no sealed event type has ever travelled unsealed, so there is no
mixed-shape window to migrate through.

## Decision

One record in four parts plus the 2026-09-03 spec-gap decisions.

### 1. Envelope (#1304)

`delivery.Body` is ONE RFC 7515 compact JWS. Its payload is the business JSON with
the single declared Subject replaced IN PLACE by an RFC 7516 compact JWE string.
Encrypt-subset-then-sign-whole: the JWS covers clear fields plus the JWE ciphertext,
never a plaintext sensitive field. Signed bytes are wire bytes — no canonicalization.
Outer protected header: `typ: vnd.gobricks.sealed.v1+json` (the authoritative,
tamper-evident version marker), `cty: application/json`, `alg` (PS256; RS256
accepted), `kid` (concrete sign Generation), `sp` (signed sealed-paths manifest,
constant per event type), `jti`, `iat`, `etyp`, `tid`. Inner JWE protected header:
`alg: RSA-OAEP-256`, `enc: A256GCM`, `kid` (concrete encrypt Generation),
`cty: application/json`, `iss` = the outer `kid` (authorship binding — kills
strip-and-re-sign; a normative MUST with a published negative vector because no stock
JOSE library checks it). One Subject in v1; always seal (nil → JWE of `null`);
sign-only deferred; outbox is persisted-sealed only. The Subject's JWE is spliced
through a shadow type so wire member order equals struct order and clear fields never
pass through a map; the `sp` member is always present (G9). AMQP `ContentType` stays
`application/octet-stream`; there is NO `x-sealed` header (S3) — the signed `typ` is
the only marker.

### 2. Tags, package, doors (#1305)

Tag `seal`, same grammar family as `jose`: a sentinel
`_ struct{} \`seal:"sign=<logical>,encrypt=<logical>"\`` and exactly one
`seal:"subject"` field whose `json` name is the `sp` entry. `json:"-"`, embedded,
`omitempty`/`omitzero` (G1), zero or multiple subjects, and subject-without-sentinel
are scan errors. Two-kid identity: both sides read the same two Logical kids and
derive their own role. Codec + scanner live in `jose/sealed` (reusing jose's tag
machinery, cryptoadapter, allowlist and error types — forbid-dual-use is a type
policy, not a code policy); the messaging adapter is `messaging/sealed`, import-gated
on the ADR-091 pattern so apps that never seal never link go-jose. Sealing engages
automatically from tags on the ADR-096 typed door; the raw consume `Handler` stays
(sealed bytes reaching it are ciphertext). A seal-tagged `T` requires the `WithMeta`
consume door at startup so the Dedup key is reachable (S4). Streams typed
declarations hard-reject seal-tagged `T` in v1; `outbox.Publish` refuses a
seal-tagged struct payload (use `Publisher[T].Seal` bytes). `PayloadStageOpen` joins
the `PayloadError` taxonomy. Logger sensitive-field vocabularies stay independent
(docs cross-reference only).

### 3. Keys (#1306, G3, G4)

Tags carry stable Logical kids; the keystore holds concrete Generation entries
`<logical>-v<N>`. Grammar: the logical part matches `^[A-Za-z0-9_-]+$`, ≤64 chars,
and may not itself end in `-v<digits>`; the keystore refuses non-conforming entries at
startup. The wire carries the concrete kid that sealed the bytes, resolved through the
untouched 1:1 `jose.KeyResolver` per message (registration-time resolution is a
check, never a cache). The Accept set IS the local keystore: the opener requires the
wire kid to be a provisioned Generation of the declared Logical family (grammar pin)
resolving locally in the inherited role. Activation is explicit:
`messaging.seal.active: {<logical>: v<N>}` on the producer — one provisioned
Generation auto-activates, several with no selector is a startup error, a selector
naming an unprovisioned Generation is a startup error; the selector's domain is every
Logical kid the producer resolves, sign and encrypt alike. Distribution is
out-of-band (no JWKS in v1). Granularity: sign family per producing service, encrypt
family per audience; per-queue forbidden; per-tenant forbidden in v1. `tid` mirrors
the ADR-087 tenant stamp into the signed header at seal time. Rotation runbooks are
per Logical kid with roles from the tag (sign: consumers get the new PUBLIC, producer
the PRIVATE, flip, drain, retire; encrypt: consumers get the new PRIVATE first,
producers the PUBLIC, flip, drain, retire). Namespace hygiene: never reuse a kid
across HTTP jose and sealing.

### 4. Replay and redelivery (#1307, G6, G7)

The seal layer judges the bytes, never the delivery history: no replay, duplicate or
freshness rejection; `inbox.ProcessOnce` (or the consumer's own idempotency) is the
sole duplicate mechanism. Slots, all mandatory and signed: `jti` — a fresh UUID minted
by the sealer on both doors, byte-stable across every redelivery; `etyp` — the
publisher declaration's `EventType`, enforced equal to the consumer's (closes
cross-type reroute); `iat` — informational, never compared to a clock; `tid` — by
tenancy (below). The Dedup key is framework-composed: `<SignFamily>:<jti>`. Every
header-sourced id (`x-outbox-event-id`) is validated against `^[A-Za-z0-9_-]{1,128}$`
before the ledger, for unsealed consumers too — `:` is outside that grammar, so a
header can never mint a sealed key (closes the shared-ledger suppression attack). The
ledger's `!inserted` short-circuit gains a dedup-hit counter and log — the only
observable of a replay campaign.

### 5. Opener rule order (G5, G7, #1308)

First failing rule wins, one code each: (1) compact JWS with the v1 `typ` else
`NOT_SEALED`; (2) `alg` allowlist, `cty` required, `crit` forbidden, unknown params
ignored; (3) kid family pin; (4) kid resolves to a PUBLIC key else
`SEAL_KID_UNKNOWN_GENERATION` (recoverable); (5) signature; (6) authenticated slots
(`jti`, `iat`, `etyp`, `sp`) else `SEAL_HEADER_SLOT_INVALID` with presence/length
detail; (7) `etyp`; (8) `tid`; (9) `sp` manifest; (10) inner JWE checks incl. `iss`
== outer kid, family pin, PRIVATE resolution, decrypt; (11) splice and decode; (12)
envelope. `tid`: shared tenancy REQUIRES a signed `tid` (absent is poison) unless the
consumer declares `TenantOptional`, then absent accepted and present equality-checked
against the carrier (G10); per-tenant tenancy — present-and-different from the context
tenant is poison, absent accepted; `multitenant.enabled: false` — no rule, value
surfaced (G2).

### 6. Greenfield premise (S1)

There is no accept-unsealed mode, no plaintext branch in the opener, no unsigned
sealing marker. Every "migration knob" clause in the 2026-09-02 records is superseded.

## Alternatives considered

- **Detached JWS in an AMQP header** — fail-open seal stripping: the header is
  strippable and the body remains valid JSON.
- **Mastercard `{"encryptedData": …}` wrapper block** — an invented shape with
  recognition-only value; their library has no signature layer to interop with.
- **Bespoke signed-context binding / destination binding** (`bnd` hash,
  duplicate-key-rejecting JSON) — security-load-bearing custom code stock JSON/JOSE
  stacks cannot provide; destination binding fights DLX parking, shovel and
  federation, and the lenient knob it forces reverts its own defense.
- **Sign-plaintext-then-encrypt-subset** (HTTP-JOSE ordering parity) — a PAN
  confirmation oracle; parity is algorithmic, not ordering.
- **`jti` recorder / freshness window** — needs an input outside the bytes; on AMQP
  the acceptance horizon is unbounded, so the recorder degenerates into a durable
  seen-set that is not transactional with the business write and rejects legitimate
  redelivery; `inbox.ProcessOnce` is strictly stronger.
- **Per-tenant keys** — unrepresentable in tags (kid is a compile-time constant), ops
  cost × tenants, and security-void in the platform topology (shared-mode producers
  hold every key).
- **`AcceptUnsealed` migration knob** — no migration exists; sealing is greenfield.
- **Tag-carries-generation** — every rotation becomes a coordinated multi-repo
  release; a Renovate bump mid-rotation delivers the new kid before ops provisions
  material.
- **Accept-list config** — allowed config-only cross-producer impersonation by
  re-binding already-provisioned foreign keys.

## Consequences

- ≈1.4 KB wire floor per sealed message (RSA-wrapped CEK + PS256, base64; prototype:
  104 B → 1505 B, nil Subject 65 B → 1435 B) — cost note for high-volume small events.
- No forward secrecy and no revocation channel (static RSA-OAEP): captured ciphertext
  stays readable until the last old private is destroyed; consumer offboarding or key
  theft = full encrypt-family rotation.
- Audience-held decrypt private.
- The consumers-before-flip gate is human-enforced until #769; violation = a DLQ
  spike with `SEAL_KID_UNKNOWN_GENERATION`.
- One producing service per sealed event type (the two-kid tag binds one sign
  family); one sealed event type per queue (`etyp` enforcement); a DLQ watcher
  declares the producer's `EventType` or uses a raw handler; an `EventType` rename is
  a coordinated release.
- `SEAL_KID_UNKNOWN_GENERATION` fires before verification — unauthenticated and
  spammable to muddy the rotation-lag signal; inherent to kid-before-verify.
- `inbox.retentionperiod` IS the replay window: a capture-then-wait replay older than
  retention re-executes if its Generation is still accepted; the rule now covers DLQ
  drains and outbox re-drives.
- The ledger has no consumer dimension (#1362, follow-up).
- A caller-side retry after exhausted in-loop retries is a new seal and a new `jti`;
  business-key idempotency remains the consumer's contract.
- A stateless sealed consumer (no ledger) leaves every replay class open; the
  `WithMeta` requirement is the structural nudge, documentation the rest.
- A keystore YAML entry binding a name to material remains a trust act — same class
  as HTTP jose today, scoped to the exact entry.
- Two ids per outbox-lane event (`record.ID` and `jti`), correlated by the persisted
  `traceparent`.
- Breaking: the header-id grammar (`fix(inbox)!`, `[C63.2]`); the typed-door break is
  ADR-096's. Sealing itself is additive and import-gated.
