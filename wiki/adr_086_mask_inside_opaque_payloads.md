# ADR-086: The Sensitive-Data Filter Masks Inside Opaque Payloads

- **Status**: Accepted
- **Date**: 2026-08-28
- **Related**: [ADR-072](adr_072_default_log_filter_names_key_material_explicitly.md) (the needle list this walks payloads with, and whose `log.sensitivefields: [keys]` remedy this makes unnecessary for JWKS) · [ADR-079](adr_079_log_filter_walks_slices_without_comparing.md) (the array walk this extends past the byte-slice leaf) · [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) (the sibling rule for a value the filter cannot judge by name)

## Context

The sensitive-data filter masks by field NAME. That works while the log line is a tree of
named Go fields, and stops working the moment a value is **opaque** — bytes or a string whose
structure the filter cannot see into (CONTEXT.md). To the filter, `json.RawMessage`,
`[]byte`, `[]json.RawMessage`, the `Bytes()` door and a plain string are single leaves,
named once by whatever key the caller chose.

Verified against the real `ZeroLogger` with the default config, every one of these logged in
clear:

- a `json.RawMessage` `{"password":"pw"}` under `body`, through `Interface`, `WithFields`
  and `Bytes` alike — the needle list contains `password`, and it never got to look;
- a root JWK's `d`, the RSA private exponent — a name no needle matches and cannot match,
  since a bare `d` needle would mask a field named `date`;
- a plain JWKS `{"keys":[…]}`. ADR-072 offers `log.sensitivefields: [keys]` for this, but
  that is opt-in, and a service that never read that ADR ships private keys by default;
- `[]json.RawMessage`, which panicked the walker before #1131 and has leaked since.

The common thread is that the name filter's guarantee quietly evaporates at the point a
consumer hands the framework a pre-encoded body — which is the ordinary way to log a request
or a response.

## Decision

**An opaque payload that parses as JSON is walked with the same needles and re-encoded
only when something was masked.**

1. **What is offered to the door.** `json.RawMessage`, `[]byte`, `[]json.RawMessage` and string
   values, at EVERY door — the check lives in the filter's shared type dispatch, so
   `Interface`, `WithFields`, `Bytes`, `Str` and any nested struct, map or slice element inherit
   it rather than each door carrying its own copy. A value is PARSED only if its first non-space
   byte is `{` or `[`; one that is not JSON-shaped is asked a single further question — whether
   it is itself a PEM private key — and otherwise returned untouched. Nothing else is parsed. A bare number, an id, a message, a non-JSON byte slice never reaches the
   decoder, so ordinary logging pays nothing for this — a benchmark pins 0 allocations for a
   non-JSON string field.
2. **Byte-exact when clean.** The payload is re-encoded ONLY if the walk masked something.
   A decode-plus-re-encode of every payload would emit a Go map's keys alphabetically, turn
   `1e3` into `1000` and round a 20-digit integer — rewriting bodies the filter had no
   reason to touch. Decoding uses `UseNumber` so a number that survives the walk keeps the
   digits it arrived with.
3. **Shape rules on top of names.** An object carrying `kty` is a JWK, and inside that object
   `d p q dp dq qi k oth` are masked wherever it sits — root, inside a JWKS `keys` array, or
   nested. Matched EXACTLY, never by substring, because the names are one and two letters
   long. A PEM block whose label ends in `PRIVATE KEY` is masked whole, both when the payload
   IS the key and when a key sits as a string member inside a JSON document — in the second
   case only that member is masked, since masking the envelope would discard every other field
   in it. A `CERTIFICATE` or `PUBLIC KEY` block is left readable, because it is public and it
   is what an operator reads to diagnose a TLS problem.
4. **Fail closed on what cannot be read.** A payload that looks like JSON and fails to parse,
   nests deeper than `DefaultMaxDepth`, or exceeds `FilterConfig.MaxPayloadBytes` is masked
   whole. Depth exhaustion is deliberately the WHOLE payload here, where the name filter masks
   only the subtree it could not reach: the name filter walks Go values this process built,
   while this door walks bytes a caller handed in, so the nesting is chosen by whoever produced
   the payload. Bounding the walk is what keeps an arbitrarily nested body from driving the
   filter down an unbounded stack on the logging path, and masking only past the cut would ship
   everything above it from a payload the filter just admitted it could not finish reading. It is opaque by definition — the filter cannot say what is inside it — and
   shipping secrets from opaque payloads is the defect being closed. A masked document that
   somehow fails to re-encode masks whole too: it must never fall back to the original bytes,
   which are the secret the walk just decided to hide.
5. **A documented cap.** `FilterConfig.MaxPayloadBytes` defaults to 64 KiB
   (`DefaultMaxPayloadBytes`) — above the bodies services log in practice, below the size at
   which decoding on the logging path becomes the expensive part of serving a request. Zero
   means the default, so a bare struct literal cannot silently opt out; a negative value is
   the explicit opt-out, restoring the pre-ADR-086 name-only behavior.

### Deliberately not inspected

- **JWTs.** A JWT is three base64 segments in a string. Recognising one means decoding it,
  and its payload is claims the operator usually needs; the private material is the signing
  key, which this ADR already covers where it appears as a JWK or a PEM block.
- **XML and form-encoded bodies.** Each needs its own parser on the logging path, with its
  own failure modes, for a shape the framework does not itself produce.
- **The log MESSAGE text.** `Msg` is a format string the caller wrote. Masking inside it
  would mean parsing arbitrary prose; the field seam is where the filter has names to judge.
  `FilterConfig.ErrorRedactor` (ADR-083) remains the seam for error text.

## Consequences

A consumer logging a pre-encoded body gets the needle list applied inside it, and a JWK or
PEM private key masked by shape rather than by a name nobody would think to configure. In
exchange, a payload the filter masks is re-encoded, so its key order and whitespace are
normalized on that path — the clean path is unchanged. A payload that is unreadable to the
filter now renders as the mask value where it used to render in full, which is a deliberate
loss of debugging detail on exactly the values that could not be checked.
