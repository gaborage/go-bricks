# ADR-152: An Over-Limit Request Body Answers 413 `BODY_TOO_LARGE` at Any Length

**Status:** Accepted
**Date:** 2026-10-25

## Context

`server.bodylimit` (default 10 MB) caps the request body, and its docs promise a 413. Which
answer a caller got depended on whether it sent `Content-Length` (#2196):

- **Known length.** echo's `BodyLimit` middleware rejects the request before the handler runs
  with `echo.ErrStatusRequestEntityTooLarge`. `customErrorHandler` kept the 413, but
  `statusToErrorCode` had no 413 arm, so the code was `INTERNAL_ERROR`. This held on typed
  routes, raw `server.Handler` routes and JOSE routes, which got the standard envelope.
- **Unknown length, typed route.** The limited reader fails mid-read, inside the bind.
  `requestProcessor.process` mapped every bind failure to 400 `BAD_REQUEST`
  "Invalid request data". On echo v5.4.0 (v0.72.0) the bind wrapped the overflow in a 400
  `HTTPError`; v5.4.1 passes it through 413-coded, behind go-bricks' `*bindError`.
- **Unknown length, raw `server.Handler`.** The handler returns its read error, which
  `classifyError` reads as a 413: `INTERNAL_ERROR` again.
- **Unknown length, JOSE route.** The JOSE read classified the overflow with
  `isRequestTooLarge` and answered `JOSE_BODY_TOO_LARGE` in the minimal pre-trust envelope.

No test or ADR pinned any of these answers.

## Decision

**Every over-limit rejection outside the JOSE pre-trust read answers 413, code
`BODY_TOO_LARGE`, message "Request Entity Too Large", with no details in production.**

- `statusToErrorCode` gains a 413 arm. Every error reaching `customErrorHandler` with a 413
  status now carries `BODY_TOO_LARGE`: the known-length rejection on every route, a raw
  `server.Handler`'s read error, and a known-length JOSE overflow, which keeps the standard
  envelope. Any other unmapped status still renders `INTERNAL_ERROR`.
- `isRequestTooLarge` moves from `server/jose.go` to `server/errors.go`. It is now the one
  predicate both the JOSE read and the bind path ask. It keeps its two arms
  (`*http.MaxBytesError`, any 413-coded echo error) and adds
  `errors.Is(err, echo.ErrStatusRequestEntityTooLarge)`, so the overflow is still seen when
  echo wraps it in a 400 `HTTPError`, as v5.4.0 did.
- `requestProcessor.process` asks the predicate before its 400 branch and returns the code,
  message and status `classifyError` derives for the bare sentinel. It adds no details,
  because a bind cause renders request input. Every other bind failure stays 400
  "Invalid request data".
- An unknown-length overflow on a JOSE route keeps `JOSE_BODY_TOO_LARGE`: the JOSE read
  reaches it before any bind, and the pre-trust envelope is deliberate.

No exported symbol is added. The framework raises this error, and handlers never need to.

## Alternatives considered

- **An exported `NewBodyTooLargeError` factory.** Rejected: the overflow is detected by the
  framework on every path, so a factory would be surface with no caller.
- **`JOSE_BODY_TOO_LARGE` everywhere.** Rejected: `JOSE_*` codes belong to the pre-trust
  JOSE envelope, and a plain JSON route answering with one misleads.
- **Match on the echo status alone.** Rejected: under v5.4.0's bind shape the status is 400,
  so the fix would silently undo itself on any echo release that wraps the overflow again.
- **Make `BodyLimit` route-aware, so JOSE and raw-response routes get their own envelopes for
  a known-length overflow too.** Out of scope; tracked in #2177.

## Consequences

- **Breaking on the wire.** A client, test or alert that matched `INTERNAL_ERROR` on a 413,
  or a 400 on an oversize chunked upload to a typed `application/json` route, now sees 413
  `BODY_TOO_LARGE`. No signature or configuration key moves.
- Two splits remain, both by length. A JOSE route answers `BODY_TOO_LARGE` in the standard
  envelope to a known-length overflow and `JOSE_BODY_TOO_LARGE` in the minimal envelope to an
  unknown-length one. A raw-response route answers a known-length overflow in the standard
  envelope, because `BodyLimit` runs before the route marks raw mode, and an unknown-length
  one in its flat shape (#2177).
- A typed route reading a `+json` media type still answers an unknown-length overflow with
  400: echo rejects the media type before reading the body (#2198).
- In a debug development deployment the known-length answer still carries
  `classifyError`'s `details.error`. The bind-path answer carries none.

## References

- `server/server.go` — `statusToErrorCode`, `classifyError`
- `server/handler.go` — `requestProcessor.process`
- `server/errors.go` — `isRequestTooLarge`
- `server/jose.go` — `joseDecodeRequestInner`
- [ADR-084](adr_084_response_error_details_carry_no_request_input.md) — the details gate
- See [migrations.md](migrations.md) `[C73.6]`.
