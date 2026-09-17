# Negative `MaxResponseBytes` on `httpclient.JOSETransport`

**Decision:** Rejected — a negative `JOSETransport.MaxResponseBytes` stays a
documented, code-only opt-in for an unbounded read when no `Envelope` is set.
It is not refused unconditionally, and it is not replaced by a named boolean.

**Reason:** The field has three documented values: zero means
`DefaultMaxJOSEBodyBytes` (10 MiB), positive is the cap, negative disables the
cap. The negative form is refused only beside an `Envelope` and an `Inbound`
policy (`JOSE_POLICY_ENVELOPE_UNBOUNDED`), because with an envelope every
eligible body is buffered before the hook can judge it. Without an envelope
the `application/jose` content-type gate decides which bodies are read at all,
so the unbounded read applies only to bodies the peer has labelled as JOSE.
The field doc, `wiki/httpclient.md`, and `llms.txt` all state this, and
`TestJOSETransportUnboundedCapRule` pins every arm by name.

What makes refusing it a regression rather than hardening:

- The value is reachable only from Go code through `Builder.WithJOSE`; no
  YAML key maps to it, so no operator can arrive at it by accident. The
  framework's negative-means-error convention (`server.bodylimit`,
  `applyNonNegativeDefault`) is an operator-facing rule for config, not a
  rule for programmatic knobs.
- The content-type gate vouches for *which* bodies are read, not for their
  size. A hostile peer that labels a multi-gigabyte body `application/jose`
  drives the same unbounded read whether the opt-in is spelled as a sign bit
  or as a named boolean. Renaming the knob changes nothing about the exposure;
  removing it removes a consumer's explicit choice.
- The #1580 protected-header bound and the #1579 fail-closed 2xx rule both
  land independently of this field.

**Reopen when either fires:**

1. `MaxResponseBytes` (or a sibling cap) becomes settable from YAML config, at
   which point the config convention applies and negative must surface as a
   `ConfigError`.
2. A second field in the framework adopts a sign bit as an unbounded opt-in.
   Two is a convention; then decide once whether it is a named boolean
   everywhere.

If retired, the natural shape is `fix(httpclient)!:` deleting the negative
arm, `Unbounded bool` beside the cap, an ADR-107 amendment, and a
`wiki/migrations.md` atom whose detect step is `git grep -n 'MaxResponseBytes: *-'`.

**Prior requests:**

- [#1600](https://github.com/gaborage/go-bricks/issues/1600) — closed
  2026-09-12 (rejected, this entry)
