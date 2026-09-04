# ADR-097: Sealed AMQP Messages — Field-Level JOSE Payload Protection

- **Status**: Accepted
- **Date**: 2026-09-03
- **Related**: [ADR-096](adr_096_typed_publish_door.md) (the typed door sealing
  engages from), [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md) (the tenant
  stamp mirrored into the signed `tid`), [ADR-091](adr_091_streams_opt_in_registration.md)
  (the import-gate pattern `messaging/sealed` follows), [ADR-090](adr_090_env_reachable_section_names.md)
  (the env-door posture the activation selector inherits), [ADR-081](adr_081_panic_type_only.md)
  / ADR-070 (why `etyp`/`jti`/`tid` values are never logged), the HTTP JOSE ADRs
  (algorithm allowlist parity)
- **Issue**: #1301 (map); decisions #1304 (envelope), #1305 (tags, package, publish
  break), #1306 (keys), #1307 (replay); #1308 (prototype); #1309 (spec-gap decisions and
  under-specification resolutions, 2026-09-03; env-door posture, 2026-09-04). Research
  branches `research/amqp-envelope-standards`, `research/amqp-seal-seams`; prototype
  `prototype/amqp-seal-open`. Deep dive: [sealing.md](sealing.md).

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

One record in six parts plus the 2026-09-03 spec-gap decisions and the
under-specification resolutions they triggered.

### 1. Envelope (#1304, G9)

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
sign-only deferred; outbox is persisted-sealed only. AMQP `ContentType` stays
`application/octet-stream`; there is NO `x-sealed` header (S3) — the signed `typ` is
the only marker.

**G9 — wire member order.** The decision is three properties, not a mechanism: (1) wire
member order equals struct order; (2) clear fields never pass through a `map` round-trip;
(3) the Subject member is always present in the sealed document — an absent member is a
typed error, never fabricated. The resolution proposed a shadow type as the means;
`jose/sealed` implements the properties by splicing the compact JWE over the Subject
member's byte span inside the framework's own `json.Marshal` output, located with a real
JSON tokenizer (nesting and strings respected), which additionally survives embedded and
promoted fields and custom `MarshalJSON` that a `reflect.StructOf` copy would drop
(`jose/sealed/splice.go`). Either mechanism satisfies G9.

### 2. Tags, package, doors (#1305, G1, S2, S4)

Tag `seal`, same grammar family as `jose`: a sentinel
`_ struct{}` field tagged `seal:"sign=LOGICAL,encrypt=LOGICAL"` and exactly one
`seal:"subject"` field whose `json` name is the `sp` entry. `json:"-"`, embedded,
unexported, `omitempty`/`omitzero` (G1: the Subject member MUST always be present), zero
or multiple subjects, and subject-without-sentinel are scan errors. Two-kid identity:
both sides read the same two Logical kids and derive their own role. Codec + scanner live
in `jose/sealed` (`ScanType`, `Seal`, `Open`; reusing jose's tag machinery, cryptoadapter,
allowlist and error types — forbid-dual-use is a type policy, not a code policy); the
messaging adapter is `messaging/sealed`, import-gated on the ADR-091 pattern: the
`messaging` package stays free of go-jose, and the gate's value is dependency hygiene plus
a loud startup error (`messaging.ErrSealingNotLinked`, "import messaging/sealed") for a seal-tagged
declaration without the import — not binary size, since `go list -deps ./app` already
carries go-jose for HTTP JOSE. `messaging/sealed`'s `init` registers its codec through
`messaging.RegisterSealCodec` (the seam is `messaging/internal/sealruntime`); `app`
configures the runtime with `messaging.ConfigureSealing` (a `messaging.SealRuntime`:
keystore, `messaging.seal.active`, tenancy, meter) before `DeclareMessaging` (modules read
it via `messaging.SealingRuntime()`), and `Declarations.Validate` reports
`ErrNotConfigured` / `ErrKeyStoreMissing` for a sealed declaration whose runtime is
incomplete. Metrics: `seal.operation.duration`
(`seal.operation = seal|open`) and `seal.open.failures.total` (`seal.error.code`). Sealing engages automatically from tags on the ADR-096
typed door; the raw consume `Handler` stays (sealed bytes reaching it are ciphertext).

Handler surface (S2): `Meta.Sealed() (SealedEnvelope, bool)` is true for every message a
seal-tagged `T` receives and false for every message a plain typed consumer receives —
per type, never per message. `Meta.DedupKey() (string, error)` returns
`<SignFamily>:<jti>` for a seal-tagged `T` and never errors; for a plain typed `T` it
returns the grammar-validated `x-outbox-event-id` or an error when absent or malformed.
`SealedEnvelope` carries `SignFamily` explicitly. A seal-tagged `T` requires the
`WithMeta` consume door at startup so the Dedup key is reachable (S4). Two envelope types,
one mapping: `messaging.SealedEnvelope` is a plain data struct in `messaging` (strings and
a time, no jose import — the import gate holds); `jose/sealed.Open` returns its own
`sealed.Envelope`; `messaging/sealed` maps one to the other. Streams typed declarations
hard-reject seal-tagged `T` in v1. The outbox flow has one shape: `bytes, err :=
h.Seal(ctx, evt)` then `deps.Outbox.Publish(ctx, tx, event)` with those bytes as the
payload; the record persists that single seal result and the relay republishes it
byte-identical on every drive, so the `jti` never changes across redeliveries — calling
`Seal` again is a new seal and a new `jti`. `outbox.Publish` refuses a seal-tagged STRUCT
payload with `outbox.ErrSealedPayloadNeedsBytes` so plaintext can never be persisted, and
`Seal` on a plain `T` is the typed error
`messaging.ErrNotSealTagged` — no rename. `messaging.PayloadStageOpen` (payloaderr stage `open`, sentinel
`messaging.ErrPayloadOpenRefused`, cause `sealruntime.OpenRefusedError`) joins the
`PayloadError` taxonomy; `outbox.Publish` names its refusal `outbox.ErrSealedPayloadNeedsBytes`
and `messaging.IsSealTagged` / `messaging.SealTagName` are the shared predicate.
Logger sensitive-field vocabularies stay independent (docs cross-reference only).

The DIRECT broker publish door has no escape hatch: the exported signature is
`Publish(ctx, client messaging.AMQPClient, evt T) error`; the bytes door is an unexported
interface inside `messaging` that `Publish` asserts, and no exported symbol lets a module
hand bytes to the broker. The outbox handoff above is the one sanctioned bytes path, and
it accepts only what `Seal` produced. Tests publish through the typed capture double the framework
ships, never a byte-capable mock (ADR-096).

**Default exchange.** An empty `Exchange` on a publisher declaration denotes AMQP's
default exchange and is exempt from the declared-exchange rule ONLY when `RoutingKey` is
non-empty (the target queue name). `Exchange: ""` + `RoutingKey: ""` names no destination
(the broker accepts and drops; publishes are not Mandatory) and is rejected at
`Declarations.Validate`, naming the event type — an omitted Exchange remains a startup
failure, not a silent drop. `JobContext.Messaging()` returns the same module-facing type
as `ModuleDeps.Messaging`; jobs publish through typed handles like modules do.

### 3. Keys (#1306, G3, G4, resolution 9)

Tags carry stable Logical kids; the keystore holds concrete Generation entries
`<logical>-v<N>`. Grammar (G4): the logical part matches the jose kid alphabet
`^[A-Za-z0-9_-]+$` (narrowed to `^[a-z0-9-]+$` by the ADR-090 reachability rule), ≤64
chars, and may not itself end in `-v<digits>`; generations are positive integers without
leading zeros — `-v([1-9][0-9]*)`, so `x-v0` and `x-v01` fail at startup, ordering is
integer comparison and the selector names the generation by that integer; the keystore
refuses non-conforming entries at startup. The wire carries the concrete kid that sealed
the bytes, resolved through the untouched 1:1 `jose.KeyResolver` per message
(registration-time resolution is a check, never a cache). The Accept set IS the local
keystore (`keystore.FamilyEnumerator`): the opener requires the wire kid to be a
provisioned Generation of the declared Logical family (grammar pin) resolving locally in
the inherited role. Activation is explicit: `messaging.seal.active: {<logical>: v<N>}` on
the producer, resolved by `keystore.ActiveGeneration` — one provisioned Generation
auto-activates, several with no selector is a startup error, a selector naming an
unprovisioned Generation is a startup error; the selector's domain is every Logical kid
the producer resolves, sign and encrypt alike. Distribution is out-of-band (no JWKS in
v1). Granularity: sign family per producing service, encrypt family per audience;
per-queue forbidden; per-tenant forbidden in v1. `tid` mirrors the ADR-087 tenant stamp
into the signed header at seal time. Rotation runbooks are per Logical kid with roles from
the tag — sign family: consumers get the new PUBLIC, the producer the PRIVATE, selector
flip, drain, retire; encrypt family (G3): consumers get the new PRIVATE first, producers
the PUBLIC, selector flip, drain, retire. Namespace hygiene: never reuse a kid across HTTP
jose and sealing; the keystore records the role tag of each resolution request
(`keystore.RoleTagJoseRoute`, `keystore.RoleTagSeal`, through `RoleRecorder`) and its
`DualRoleReporter` WARNs at startup when one entry is resolved under both (#1363).

**Env door.** `messaging.seal.active` keys are Logical kids; the koanf env transform is
one global injective function (ADR-024/ADR-090), so an env override reaches `[a-z0-9]`
kids everywhere, hyphenated kids only where the runtime permits `-` in variable names
(Docker and Kubernetes manifests yes, POSIX `export` no — the posture ADR-090 states for
`keystore.keys`), and is otherwise YAML-only. A per-key `_`→`-` mapping was considered
and rejected: it would need a second transform or a post-load rewrite and reintroduces
the `a_b`/`a-b` ambiguity the reachability rule forbids. Selector keys are constrained to
`[a-z0-9-]` at `Validate`, matching `keystore.keys`.

### 4. Replay and redelivery (#1307, G6, G7)

The seal layer judges the bytes, never the delivery history: no replay, duplicate or
freshness rejection; `inbox.ProcessOnce` (or the consumer's own idempotency) is the
sole duplicate mechanism. Slots, all signed; `jti`, `iat` and `etyp` always present, `tid`
present exactly when the producer carried a tenant stamp: `jti` — a fresh UUID minted
by the sealer on both doors, byte-stable across every redelivery; `etyp` — the
publisher declaration's `EventType`, enforced equal to the consumer's (closes
cross-type reroute); `iat` — informational, never compared to a clock; `tid` — by
tenancy (below). The Dedup key is framework-composed: `<SignFamily>:<jti>` — the Logical
family, never the concrete Generation, so a rotation does not re-open the replay window.
Every header-sourced id (`x-outbox-event-id`) is validated against
`^[A-Za-z0-9_-]{1,128}$` before the ledger, for unsealed consumers too — `:` is outside
that grammar, so a header can never mint a sealed key (closes the shared-ledger
suppression attack). The grammar governs header-SOURCED ids only: the sealed key is
framework-minted, carries its `:` on purpose, and `inbox.ProcessOnce` admits it under the
delivery context the sealed door marks (`messaging.IsSealedDelivery`), so the two key
spaces never collide and neither path can spell the other's key. The ledger's `!inserted` short-circuit gains a dedup-hit counter and
log — the only observable of a replay campaign.

### 5. Opener rule order (G5, G7, #1308)

First failing rule wins, one code each — the table is in [sealing.md](sealing.md#opening-rule-order)
and the constants in `jose/sealed/open.go`: (1) compact JWS with the v1 `typ` else
`NOT_SEALED`; (2) `alg` allowlist, `cty` required, `crit` forbidden, unknown params
ignored (G5); (3) kid family pin; (4) kid resolves to a PUBLIC key else
`SEAL_KID_UNKNOWN_GENERATION` (recoverable); (5) signature; (6) authenticated slots
(`jti` presence + grammar, `iat` present integer NumericDate non-negative, `etyp`
non-empty, `sp` non-empty array) else `SEAL_HEADER_SLOT_INVALID` with a `slot` detail
carrying presence and length only (G7); (7) `etyp`; (8) `tid`; (9) `sp` manifest;
(10) inner JWE checks incl. `iss` == outer kid, family pin, PRIVATE resolution, decrypt —
the inner layer reuses the outer codes with a `layer: jwe` detail, and an unparseable
payload document or a non-string Subject member is `SEAL_PAYLOAD_UNDECODABLE`; (11) splice
and decode; (12) envelope. Rules 1–4 run on the peeked, still unauthenticated protected
header. `tid` truth table (rule 8): shared tenancy — a signed `tid` is REQUIRED, absent is
poison, present is equality-checked against the carrier's tenant; shared tenancy with the
consumer declaring `TenantOptional` — absent is accepted, present is equality-checked when
the carrier carries a tenant and surfaced uncompared on `Meta.Sealed().TenantID` when the
delivery is unstamped (G10, #1359); per-tenant tenancy — present-and-different from the
context tenant is poison, absent accepted; `multitenant.enabled: false` — no rule, value
surfaced (G2).

### 6. Greenfield premise (S1)

There is no accept-unsealed mode, no plaintext branch in the opener, no unsigned
sealing marker. Every "migration knob" clause in the 2026-09-02 records is superseded;
the prototype asset that still shows the knob stays as-is (throwaway, dated).

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
- **Shadow type for the splice** — satisfies G9 but drops embedded/promoted fields and
  custom `MarshalJSON`; the tokenizer splice keeps them.
- **Per-key `_`→`-` env mapping for the selector** — a second transform that reintroduces
  the `a_b`/`a-b` ambiguity ADR-090 forbids.

## Consequences

- ≈1.4 KB wire floor per sealed message (RSA-wrapped CEK + PS256, base64; prototype:
  104 B → 1505 B, nil Subject 65 B → 1435 B) — cost note for high-volume small events.
- Ciphertext length reveals Subject length: the JWE grows with the plaintext, so a
  reader of the broker learns the size class of the sealed value (informational).
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
  retention re-executes if its Generation is still accepted; the rule covers DLQ
  drains and outbox re-drives, and producer and consumer retention live in different
  processes, so only a documented rule and a same-process WARN are possible.
- The ledger has no consumer dimension (#1362, follow-up).
- A caller-side retry after exhausted in-loop retries is a new seal and a new `jti`;
  business-key idempotency remains the consumer's contract.
- A stateless sealed consumer (no ledger) leaves every replay class open; the
  `WithMeta` requirement is the structural nudge, documentation the rest.
- `inbox.ProcessOnce` admits a sealed Dedup key only under the delivery context the sealed
  door handed the handler (`messaging.IsSealedDelivery`): derive from the handler's context
  (`context.WithoutCancel(ctx)` for background work, never `context.Background()`) or the
  marker is lost and the call fails closed with `ErrInvalidEventID`.
- Shared tenancy with `TenantOptional`: an unstamped delivery carrying a signed `tid` is
  accepted and the `tid` is surfaced on `Meta.Sealed().TenantID` without comparison; a
  consumer that cares refuses in the handler on `env.TenantID`.
- Rotation-overlap replay is true at the seal layer and irrelevant at the effect layer:
  an old-generation replay lands in the ledger as a duplicate of the same key.
- A keystore YAML entry binding a name to material remains a trust act — same class
  as HTTP jose today, scoped to the exact entry.
- Two ids per outbox-lane event (`record.ID` and `jti`), correlated by the persisted
  `traceparent`.
- Breaking: the header-id grammar (`fix(inbox)!`, [migrations.md](migrations.md)
  `[C63.2]`); the typed-door break is ADR-096's (`[C63.1]`). Sealing itself is additive
  and import-gated.
