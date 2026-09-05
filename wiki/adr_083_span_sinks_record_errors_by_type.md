# ADR-083: Every framework span sink records an error by type, through one helper

- **Status**: Accepted
- **Date**: 2026-08-24
- **Related**: [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) (the same rule for recovered panic values, which this generalizes to every error) · [ADR-079](adr_079_log_filter_walks_slices_without_comparing.md) (the field-name matcher whose reach is the whole issue) · [ADR-068](adr_068_delivery_pipeline.md) (a service driving its own consume loop starts its own span, so the helper is exported)

## Context

`span.RecordError(err)` ships `err.Error()` to the tracing backend as an
`exception` event, and every site that called it also copied the same message
into the span status description. Both are **off-platform sinks**: their
retention, access model and export path belong to the tracing vendor, and the
logger's `SensitiveDataFilter` never sees either one.

The errors reaching them are consumer-authored. A job's `Execute` error, a
message handler's error, an HTTP response interceptor's error, a
caller-supplied `RoundTripper`'s error — each is a string the framework did not
write and cannot constrain. Field-name masking cannot help: the key is fixed
(`exception.message`), and the secret is inside the value, so closing this needs
value-shaped redaction rather than another needle.

Two sites had already reached that conclusion independently and fixed it in
place:

- `database/internal/tracking` hand-built its exception event with the driver
  error's `%T`, because Postgres and Oracle echo the offending row value back in
  a unique-constraint message.
- `httpclient/internal/tracking` hand-built its own, with a message run through
  a `redactErrorMessage` helper that stripped a `*url.Error`'s query string and
  userinfo — Go's stdlib redacts the userinfo password and never the query
  string, so a failed `GET …?token=secret` carried the token.

Both are the same decision made twice, in two spellings, and the httpclient one
still trusted the rest of the stringification: the URL was the leak that had
been *found*, not the only one a message can carry. ADR-081 made the same call
for recovered panic values and deliberately deferred value-aware redaction of
arbitrary consumer values.

Four sites still exported the message verbatim: the scheduler's job-error path,
the delivery pipeline's handler-error path (both the classic AMQP lane and the
streams lane, which share `delivery.Run`), the HTTP client's span end, and AMQP
and stream publish failure.

## Decision

**Every framework span sink reports an error by its Go TYPE, through one
exported helper that is the only spelling of "record an error on a span" in
framework code.**

"Sink" means the SPAN, not one API on it. An exception event, a status
description and a plain span attribute all leave with the same exporter under
the same retention, so a rule written against `RecordError` alone is a rule with
a hole in it — the AMQP `publish.retry` event proved it, carrying the broker's
message in an ordinary `attribute.String("error", …)` while the terminal status
beside it was already type-only.

```go
observability.RecordErrorByType(span trace.Span, err error)
```

It is a no-op on a nil span or a nil error; otherwise it adds one `exception`
event carrying `exception.type` = `fmt.Sprintf("%T", err)` and **no**
`exception.message`, and sets the status to `codes.Error` with the same string
as the description.

The type is the **outer** one — no unwrap walk, no list of types — the same
spelling ADR-081 and the database site already use. A root-cause type would be
more informative and is a second decision to make later; the value of one rule
here is that a reader can predict what a span says without reading the site.

Site-specific rules:

- **HTTP client**: when a classified error-type label (`transport_error`,
  `interceptor_failed`, …) is present it stays the status description — that is
  framework vocabulary, not consumer text. When absent, the description is the
  Go type, never the message. A 5xx still outranks both. `redactErrorMessage`
  becomes dead and is deleted with its tests.
- **Database tracking**: the inline event construction is replaced by the
  helper. The log-side `error_type` field is unchanged.
- **AMQP publish retries**: the `publish.retry` event carried the broker's
  server-authored `Reason` verbatim in an `error` attribute. It now names the Go
  type under `error.type`; the WARN log line beside it keeps the message.
- **Scheduler panic path**: `panicErr` (already type-only per ADR-081) goes
  through the helper, and the status description stays `"panic"` — both are
  framework-shaped, and `"panic"` is the more useful of the two. `panicErr` is
  built by the framework, so its own `%T` says nothing; the datum an operator
  wants is the RECOVERED value's type, and it reaches the span as its own
  attribute, `job.panic_type` — the replacement for the
  `exception.message = "panic (type: T)"` this hop removes.
- **Log lines at every converted site are unchanged.** They keep the message: a
  log field is an on-platform sink the operator's retention owns and the
  sensitive-data filter reads, and `FilterConfig.ErrorRedactor` (#1183) lets the
  consumer redact it by content. The two sinks want opposite answers — the log
  keeps the message and hands the operator a hook, the span drops it — and this
  ADR decides only the off-platform one.

The helper is exported because a service driving its own consume loop (ADR-068)
starts its own span and should have the safe call available. It is **offered,
not enforced** — a span a consumer started is the consumer's to record on.

## Consequences

Operators lose the error message from four span sinks. An alert or dashboard
keyed on `exception.message`, or on a message-bearing status description, for
job failures, handler failures, HTTP client failures or publish failures stops
matching — silently, since the attribute is absent rather than empty. The Go
type is the replacement for grouping; the message is still on the log line at
every one of those sites, where `FilterConfig.ErrorRedactor` can further redact it by content.

The one-helper rule is checkable rather than remembered, but the check needs a
POSITIVE control: the helper spells the event and the attribute from `semconv`,
so a grep for the literal `"exception"` now matches nothing anywhere and cannot
tell "the invariant holds" from "the grep is looking for a spelling nobody
uses". Grep for the spellings a new sink would actually reach for —
`git grep -nE 'span\.RecordError\(|semconv\.Exception' -- '*.go' ':!*_test.go'`
— and expect production hits ONLY in `observability/span_error.go`. That grep is
necessary and NOT sufficient: it finds the exception-event spelling, and an
`attribute.String("error", err.Error())` on any span passes it untouched. Pair it
with `git grep -nE 'attribute\.[A-Za-z]+\([^)]*[Ee]rr' -- '*.go' ':!*_test.go'`
and read every hit — each one must render a classification, never a message. Two
legitimate neighbours also surface and are not sinks: `observability/provider.go`'s
`WithoutPanicRecording`, which exists to STOP the SDK recording a panic as an
exception event (the same rule one layer down), and
`observability/testing/helpers.go`, whose `AssertExceptionTypeOnly` /
`AssertNoExceptionEvent` READ what a sink wrote — they are the assertion every
converted site shares, so the property is pinned in one place too. That half is
now enforced rather than remembered: `forbidigo` in `.golangci.yml` fails
`make check` on both spellings. The only exemptions are `_test.go` and inline
`//nolint`s on the individual lines of the helper and of the reader-side
assertions — inline, because a standalone directive above a call expands to the
whole statement and would silently cover a message attribute added beside the
type one, and per-line, so a second function in either file has to earn its own.
The `RecordError` pattern resolves through the `trace.Span` INTERFACE, so a sink
holding a concrete span type would expand to a different name and slip past it;
no such site exists today.

The attribute-shaped half stays a grep because forbidigo matches IDENTIFIERS and
the leak is an ARGUMENT SHAPE — `attribute.String(k, err.Error())` — which it
cannot express; `^error\.Error$` would fire on every log line in the repo. Not
because a rule would be noisy: all thirteen live sites render a classification,
so a leak-shaped rule would be silent today. Writing one wants a `gocritic`
ruleguard pattern, which is its own change.

`fmt.Sprintf("%T", err)` allocates on a path that only runs on failure, which is
the right place to spend it.

Migration: [migrations.md](migrations.md) `[C61.3]` · issue #1132.
