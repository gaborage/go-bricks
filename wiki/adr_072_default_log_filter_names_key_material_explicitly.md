# ADR-072: The default log filter names key material explicitly, not by a bare "key"

- **Status**: Accepted
- **Date**: 2026-08-20
- **Related**: [ADR-070](adr_070_inbound_trace_identifier_validation.md) (the other seam where a value's shape decides whether it survives to a log line) · `wiki/observability.md` (the consumer-facing list and the two extension seams)

## Context

`logger.DefaultFilterConfig` masks a value when its **field name** matches a
needle, case-insensitively, by substring. The list carried a bare `key`.

Substring matching makes that needle far wider than key material. Every field
whose name merely contains the word is masked: `keys`, `tenant_key`,
`cache_key`, `routing_key`, and the plain `key` the framework itself logs.
Fifteen of the framework's own log sites log the bare field name `key` — nine in the
app factory resolver, four in the messaging manager, one each in the database
manager and the server handler. Fourteen log a tenant or resource identifier
under it; the fifteenth logs the NAME of a reserved envelope-meta key a handler
tried to overwrite. Every one renders `***`, and so do the eight `routing_key`
sites in the AMQP client and registry, and `dropped_meta_keys`.

The loss is one-way and unrecoverable at the consumer's end. The YAML seam
(`log.sensitivefields`) only *adds* needles; there is no removal. A consumer who
wants `tenant_key` in their logs has to abandon the default list wholesale
through `app.Options.LoggerFilterConfig` and re-supply every entry, which is
precisely the mistake that seam already warns about. So the bare needle taxes
observability for everyone and offers no dial.

What it did buy is real, though: it incidentally covered `private_key`,
`signing_key`, `encryption_key` and their concatenated spellings. Removing it
without replacement would put actual key material in the clear.

## Decision

Name the key-material shapes explicitly, then drop the bare needle.

Added: `private_key`, `privatekey`, `private-key`, `signing_key`, `signingkey`,
`signing-key`, `encryption_key`, `encryptionkey`, `encryption-key`, `api-key`.
Already present and kept: `api_key`, `apikey`.

Three spellings of each, because the matcher gives no relationship between them:
`api_key` does not contain `apikey`, and neither contains `api-key`. One needle
covers only the exact byte sequence, wherever it appears.

The hyphenated ones are not hypothetical tidiness. `httpclient` logs whole
`http.Header` maps through this filter under `LogPayloads`, and the header is
spelled `X-Api-Key` — which the bare `key` masked and no underscore needle does.
A single `-key` needle would cover every such header, and would also mask
`Idempotency-Key` — an identifier consumers send on every payment POST, and
exactly the over-masking this change exists to end. Naming the shape rather than
the word is the same rule this ADR applies everywhere else.

`secret_key` and `secretkey` are deliberately NOT added. Both contain `secret`,
which is already a needle, so entries would be dead weight that reads as
coverage. That asymmetry is worth stating rather than leaving for a reader to
re-derive from the matcher.

Identifiers — `key`, `keys`, `tenant_key`, `cache_key`, `routing_key` — now log
in clear, and the framework's fifteen sites emit their identifiers without a
rename.

## Alternatives considered

**Rename the framework's log fields `key` → `name`/`id`.** Rejected during
triage. It fixes the framework's own fifteen lines and leaves every consumer
field — `tenant_key`, `cache_key` — masked, so the general problem survives and
the framework's log field names become a compatibility surface.

**Exempt by suffix or prefix: mask `*_key` but not `key`/`keys`.** Fragile in
both directions. `license_key` is a secret and `routing_key` is not, and no
affix rule separates them; it also introduces a second matching mode into a
filter whose one rule today is "substring", which is the property that makes the
list auditable.

**Keep the needle and add a removal seam to `log.sensitivefields`.** A larger
API — subtraction against a list the framework may change under you, with the
failure mode of silently un-masking something a later release adds. The list is
the contract; editing what is *in* it is the honest change.

**Do nothing; tell consumers to name identifiers `id`.** This is what the code
comment said before. It does not survive contact with an existing service, and
it puts the framework in the position of dictating field names to avoid its own
default.

## Consequences

**Positive.** Fifteen framework log sites regain their identifiers with no
rename. Consumer fields named `*_key` that are identifiers stop being masked.
The list now says what it covers instead of relying on a word that happens to
appear inside key material.

**Negative — this un-masks.** A consumer relying on the bare needle to mask a
field the new list does not name — `license_key`, `hmac_key`, `master_key`,
`session_key`, or a vendor header such as `Ocp-Apim-Subscription-Key` — starts
logging that value in clear on upgrade, with no error and no warning.

One shape in that class deserves naming on its own, because it does not read as
a secret at all: `keys` is the JWKS container. The bare needle stopped the filter's walk at that
field; now it recurses, and a JWK's `d` — the RSA private exponent — matches no
needle. That reaches a log only through `httpclient`'s `LogPayloads`, which is
off by default and documented dev-only, but a service that turns it on while
fetching a PRIVATE key set logs the private material. Add `keys` to
`log.sensitivefields` if that is your shape. That is the whole risk of this change and it is why it
ships with a migration atom rather than as a quiet default tweak; the remedy is
one line of `log.sensitivefields`. Documented as `[C60.13]`.

**Neutral.** Nothing about the matcher changes: still case-insensitive, still
substring, still applied at the same seam. Only the list moved.

## Addendum (2026-08-21, ADR-079)

The JWKS paragraph above is wrong about the failure mode, and its remedy is
narrower than it reads.

**It was not a leak — it was a panic.** A JWKS body is `{"keys":[{…}]}`, a JSON
array of objects. The walk into it did not log the `d` exponent; it crashed the
log path, because the filter compared two `any` values to decide whether to
preserve the slice's concrete type and those values are uncomparable. The leak
this ADR describes is real only for a JWK at the body ROOT, or for a JWKS reached
through a path that does not go through a slice. Fixed in
[ADR-079](adr_079_log_filter_walks_slices_without_comparing.md); with the walk
repaired, the leak this paragraph describes is now the actual behaviour for the
array shape too.

**`log.sensitivefields: [keys]` fixes one spelling.** Matching stays what this ADR
says it is — case-insensitive SUBSTRING — so that needle covers `keys`, `KEYS`,
`api_keys`, `public_keys` and, incidentally, `monkeys`. What it does not reach is
the shape that matters: a single JWK at the body root has no `keys` wrapper,
`jwk` is a different name, and every `{"data":[…]}` or `{"items":[…]}` envelope
carrying key material is untouched. The remedy is generous about spellings of one
container name and blind to every other container. The needle list is the wrong
instrument for this — the field that matters is `d`, a name too short and too
common to add — which is why ADR-079 records document-shape recognition
(JWK/JWKS/PEM/JWT: masking by position and neighbours rather than by name) as a
separate decision with its own ADR still to be written, rather than folding it in.

The `[C60.13]` atom is unchanged: the un-masking it describes is real, and the
one-line remedy still applies to the shape it names.
