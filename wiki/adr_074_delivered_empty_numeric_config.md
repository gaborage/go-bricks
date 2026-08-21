# ADR-074: A delivered-empty numeric config value fails startup

- **Status**: Accepted
- **Date**: 2026-08-20
- **Related**: [ADR-051](adr_051_delivered_empty_database_identity.md) (the delivered-empty rule this extends from database identity keys to numeric keys) · [ADR-065](adr_065_keystore_secretminlength_tristate.md) (the tri-state this silently defeated)

## Context

`FOO=` in a Kubernetes manifest, an empty `secretKeyRef`, an `envsubst` over an unset
variable — every one of them delivers a set-but-empty string. koanf keeps the key
(with its empty value), and mapstructure's `WeaklyTypedInput` rewrites `""` to `0`
for any numeric target. The result decodes as a legal zero.

Measured against `main` before this change:

- `KEYSTORE_SECRETMINLENGTH=` produced `SecretMinLength = *0`, which **disables the
  secret-length floor**. ADR-065's tri-state cannot defend itself here: normalization
  fills a *nil* pointer, and the decoder handed it a non-nil pointer to zero, which
  reads as "the operator chose 0".
- `SERVER_BODYLIMIT=` produced `0`, rescued only by a downstream `<= 0` fallback.

The damage is confined to numeric keys where `0` is legal. Range-validated ints
(`SERVER_PORT=`), enums (`LOG_LEVEL=`), and durations (`SERVER_TIMEOUT_READ=`) already
fail loudly, because zero is rejected downstream or the string will not parse.

Worth stating plainly, because it sets the cost of this change: for MOST of those keys
the old behaviour was not broken. Across the framework `0` conventionally means
"unset — use the default", so an empty value landed on the documented default and the
deployment was genuinely healthy — `cache.redis.port` resolved to 6379, `outbox.batchsize`
and `messaging.reconnect.maxpublishattempts` to their defaults, `database.manager.maxsize`
likewise. Only where `0` is a legal, meaningful value — the secret-length floor, the body
limit — did an empty value quietly change behaviour. This ADR therefore turns a set of
working deployments into failing ones, deliberately: the framework cannot tell "I meant
the default" from "my template rendered nothing", and guessing has already produced one
security-relevant silent flip.

One constraint fell out of verification and shapes the whole decision: the fix must
NOT prune empty keys from the koanf tree. ADR-051's database-identity check reads key
*presence* (`Config.Exists`), so dropping a delivered-empty key would boot the exact
misconfiguration ADR-051 exists to catch — reading it as "no database configured".

## Decision

The rejection lives at the decode layer, not in the tree. A mapstructure hook
(`configdecode.EmptyStringToNumericGuardHookFunc`) runs ahead of the weak `""` → `0`
coercion in both decoder seams — `buildDecoderConfig` (used by `config.Load`) and
`unmarshalDecoderConfig` (the public `Config.Unmarshal`) — and fails the decode when
an empty or whitespace-only string is bound to a numeric field. Pointer targets are
included: `*0` is precisely the shape that defeats a tri-state. mapstructure attaches
the key it was decoding, so the operator gets the key name with the message.

`WeaklyTypedInput` stays: string → bool and the other coercions are still wanted.
`time.Duration` is exempt, because `StringToTimeDurationHookFunc` owns that target and
already fails loudly on an empty string — guarding it here would only change which
loud error appears. Non-numeric targets are untouched: an empty string is a legal
string, and the identity subset of them belongs to ADR-051, which still fires first
for `DATABASE_HOST=` because those keys are string-typed.

YAML **null** (`secretminlength:` with no value) is deliberately out of scope. It is
different plumbing — koanf delivers a nil value, not `""` — and it already behaves as
absence: the key takes its default rather than a silent zero. A test pins that
boundary so it cannot drift unnoticed.

Four seams carry the hook: the two above, `migration.decodeSecretConfig`, and the
`tools/migration` CLI's `tenantDecoderConfig`. The third builds its own decoder for a
dynamic `DBConfigProvider` payload — the seam that
literally reads secrets, where a rotated secret rendering `{"port": ""}` would dial
port 0 and `{"pool":{"max":{"connections":""}}}` would silently revert a tuned pool to
the framework default. The fourth is the CLI's, which keeps a byte-identical local copy of
the hook rather than importing it. The import would in fact compile — Go's internal
rule is import-path-prefix based and the CLI sits under `github.com/gaborage/go-bricks/`
— but a copy keeps that separately-released binary off a package that carries no
compatibility guarantee. The copies must be kept in sync by hand, and a source-comparison test in
`internal/configdecode` now fails when they diverge — a gate that holds whichever way #1109
decides, since it neither imports the package nor assumes the copy stays.

An empty string is judged after trimming, so a whitespace-only value is rejected too.
That one is a message change rather than a new failure — `"   "` already failed to
parse — but it keeps one rule for what "delivered empty" means.

## Consequences

- `FOO=` no longer means `0` for a numeric key. A deployment that relied on it — or
  that has an empty `secretKeyRef` it never noticed — fails startup naming the key.
  For keys where `0` resolves to a default this ends a posture that was working; the
  trade is a loud failure now against an unnoticed one later, and the operator is the
  only one who knows which value was intended.
- `observability.*` decodes through the public `Config.Unmarshal` seam, not through
  `config.Config`, and `app` used to swallow a decode failure there with one WARN and
  fall back to the no-op provider — trading a single bad key for total telemetry loss
  (no traces, no metrics, no OTLP logs, no `migration.applied` audit events). This guard
  makes that shape reachable from a rendered-empty value, so the seam now separates the
  two cases: an ABSENT section keeps the no-op posture, a section that is present but
  undecodable aborts startup.
- `KEYSTORE_SECRETMINLENGTH=` is now a startup error rather than a WARN plus a
  disabled floor. `KEYSTORE_SECRETMINLENGTH=0` is unchanged — the explicit,
  deprecated opt-out still works.
- Unset variables, YAML omission, YAML null, and explicit values behave exactly as
  before.
- The public `Config.Unmarshal` seam enforces the same rule, so a consumer's own
  config struct gets it without opting in — including its slice and map ELEMENTS, where
  an empty element used to decode as a zero entry.
- Deliberately not covered, and worth naming so the sweep is not read as complete:
  `""` bound to a `*bool` still decodes as a non-nil `false`, which defeats
  `cache.critical`'s tri-state the same way `*0` defeated the secret floor (#1110); and
  the typed getters `Config.Int`/`Int64`/`Float64`/`Bool` still return their default for
  a present-but-empty key rather than reporting it (#1111). Both are the same defect
  class at different seams; neither is numeric-decode, which is what this ADR closes.

Migration: [C60.15](migrations.md).

## Alternatives considered

**Drop empty values in the env `TransformFunc`.** The obvious fix, and it breaks
ADR-051: with the key gone from the tree, a delivered-empty `DATABASE_HOST` reads as
a database-free deployment and boots green — the failure ADR-051 was written to stop.

**Turn `WeaklyTypedInput` off.** It would take the coercion away along with string →
bool and every other conversion the framework's env-var surface depends on. The
problem is one source/target pair, not weak typing.

**Let each key defend itself downstream.** That is the status quo, and it is why the
bug is uneven: `SERVER_BODYLIMIT` happened to have a `<= 0` fallback and
`KEYSTORE_SECRETMINLENGTH` did not. A rule that every numeric key has to remember is
a rule that some numeric key will forget.
