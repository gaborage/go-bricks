# ADR-084: Response error details carry no request input

- **Status**: Accepted
- **Date**: 2026-08-24
- **Related**: [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) (`[C60.30]` put the 5xx body's `details.error` behind `app.debug` AND a development env; this ADR finishes that posture at every status) · [ADR-001](adr_001_enhanced_handler_system.md) (the "details in development" rule this narrows) · [ADR-022](adr_022_env_policy.md) (the alias sets both gates read)

> **Amended (2026-09-05):** the JOSE post-trust error envelope is no longer the
> exception this ADR recorded. `server/jose.go`'s `buildErrorEnvelope` discarded its
> `*config.Config` and copied `IAPIError.Details()` to the wire ungated; it now renders
> `details` through `devDetails`, so all three renderers — enveloped, raw and JOSE —
> share one gate.
>
> Two sentences in the Decision below are SUPERSEDED by this amendment and are kept
> only as the historical record:
>
> > Both response renderers — the standard envelope and raw mode — already funnel
> > through it, so the predicate lives there once and applies to every status rather
> > than only the 5xx path `[C60.30]` reached.
>
> Three renderers funnel through it now, not two.
>
> > The JOSE envelope is the third renderer and stays ungated here: it is encrypted to
> > an authenticated peer, and unifying the three is #1163's job.
>
> That job is done, in this amendment's change; the JOSE envelope is gated exactly like
> the other two.
>
> The original reasoning was that ciphertext to an authenticated peer is not
> disclosure. That does not hold: the peer decrypts the body and routinely logs it, so
> an ungated envelope pushed handler-set details, bind and validation diagnostics, and
> a captured `stackTrace` into the peer's log estate. Encryption bounds who reads the
> body, not what the body may say.
>
> `formatJOSEPlaintextError` is deliberately NOT routed through the funnel: the
> pre-trust envelope is built from a `joseAPIError`, whose `Details()` returns nil by
> construction, so it has no key to gate and adding a second gate would only invite the
> two to drift. `classifyError`'s attach-side gate is likewise kept, as defense in
> depth — it bounds what reaches `details` before any renderer sees it.
>
> The wire change ships as `[C64.2]`: a production JOSE error envelope carries `code`,
> `message` and `meta` only, and a peer parsing `error.details` outside
> debug + development stops receiving the key.

## Context

Two of the framework's own 400 details echoed caller-controlled text verbatim.

**The validation path** (`server/validator.go`). `NewValidationError` built each
`FieldError` as `{Field: err.Field(), Message: getErrorMessage(err), Value:
fmt.Sprintf("%v", err.Value())}`, and the whole list went out as
`Details("validationErrors")`.

- `Value` is the rejected input, for ANY failed tag. A `min=8` failure on a
  password field echoed the password.
- `Field` is not schema for a `dive`-validated map: go-playground interpolates
  the input map key into the namespace verbatim, so a request carrying
  `{"limits":{"4111111111111111-SECRET":1}}` produced
  `Limits[4111111111111111-SECRET]` — and `getErrorMessage` interpolated the
  same string a second time into the human-readable message.

**The bind path** (`server/handler.go`). A bind failure returned
`WithDetails("error", bindErr.Error())`. For JSON that is the decoder's own
text — which under a Go 1.27 build reports a map destination's field path as
`limits.<input key>` — and for query/path/header binding it is a strconv error,
which quotes the rejected value (`parsing "not-a-number"`).

Both were gated on `cfg.App.IsDevelopment()` alone, looser than the posture
`[C60.30]` had just established for the 500 path, and response bodies never pass
through the logger's `SensitiveDataFilter` — that filter is a log-path
component, and it matches by field NAME, which is exactly what a hostile map key
does not have.

`messaging` had already solved this class for the same inputs on the AMQP side:
a type-gated decode summary that renders schema facts only, and a bracketed-span
namespace redaction (`Limits[4111…]` → `Limits[*]`). Both were package-private
there, so the HTTP path re-derived nothing and leaked instead.

## Decision

**One package owns the safe-rendering primitives.** `internal/saferender` holds
`RedactNamespace`, `FieldPathIsSchema` and `JSONDecodeSummary`, moved out of
`messaging` unchanged. `messaging` consumes them from there; its exported
surface, its rendering and its tests' assertions are untouched. A second
transport that renders a decode or validation failure now has one place to
consume, and the rules cannot drift between the two.

**`FieldError` loses `Value` entirely** rather than gaining a redaction. The
value is request input for every tag, so there is no shape-based rule that keeps
it and stays safe — the field is the defect, not its rendering. `Field` and the
message it feeds are both built from `RedactLeafField(err.Namespace())`, so a
dived map's element reads `Limits[*]` in both.

The name is derived from the NAMESPACE, not from validator's own `Field()`.
validator stores that field's length in a `uint8` and slices the namespace by it
(`validator/v10` `errors.go`), so a namespace longer than 255 bytes wraps and
`Field()` returns an arbitrary suffix of the namespace — for a dived map, a
suffix of the caller's own key, carrying no `[` for any bracket rule to find. The
key is caller-sized, so the caller picks where that cut lands: a 250-byte filler
plus a PAN returned the PAN in clear. `Namespace()` does no such arithmetic.

**Bind failures render a summary, never the cause.** A `*bindError` pairs the
raw cause with a payload-free summary: for a JSON body, `JSONDecodeSummary`
under the request type's own `FieldPathIsSchema` gate, decided once per route;
for query/path/header, the binding source plus the destination field named by
its struct tag — author-written schema, never the input. `Error()` still renders
the cause, so request-completion logging is unchanged (the log-side
redactor is issue #1168); the response path reads `bindSummary`, which fails closed on any error
that is not a `*bindError`.

**The details gate is `Debug && IsDevelopment`, at `devDetails`.** Both response
renderers — the standard envelope and raw mode — already funnel through it, so
the predicate lives there once and applies to every status rather than only the
5xx path `[C60.30]` reached. The JOSE envelope is the third renderer and stays
ungated here: it is encrypted to an authenticated peer, and unifying the three
is #1163's job. What this ADR guarantees for that path is the content — a
`FieldError` and a bind summary that hold nothing to leak in the first place.

## Consequences

- **Breaking**: `server.FieldError.Value` is gone. apidiff reports INCOMPATIBLE;
  a consumer reading it no longer compiles. There is no replacement — the value
  was the leak. `[C61.1]`.
- A development deployment running `app.debug: false` stops seeing ANY
  `error.details` — validation errors and stack traces included, at every
  status, on both renderers. Setting `app.debug: true` restores them. `[C61.2]`.
- The 400 body for a bind failure is less precise than the raw decoder text it
  replaces. A map-bearing request type renders no field path at all, which is
  the fail-closed price `FieldPathIsSchema` charges — and a request struct
  carrying a `time.Time` pays it too, since `time.Time` is a `json.Unmarshaler`.
  The raw cause is one `errors.Unwrap` away for a caller that wants it, and it
  is still in the log line.
- `getErrorMessage` takes the already-redacted field as a parameter instead of
  reading `fe.Field()` again. That signature is what makes the leak
  non-reintroducible by a future case arm: there is no un-redacted name in
  scope.

## Alternatives considered

**Redact `Value` instead of removing it.** Rejected: redaction needs a rule, and
every rule here is shape-based — a length floor, a charset, a PAN detector — on
a value whose meaning is the consumer's. `min=8` on a password and `gte=100` on
an amount are indistinguishable to the framework.

**Keep the gate at `IsDevelopment` and rely on the content fix.** Rejected. The
content fix covers the framework's own entries; the gate also bounds a handler's
own `WithDetails`, which the framework cannot audit. Two independent defenses,
and `[C60.30]` had already chosen this pairing for the sibling path.

**Leave the primitives in `messaging` and import them from `server`.** Rejected:
`server` importing `messaging` inverts the dependency direction and drags an
AMQP client into every HTTP-only service's build graph.
