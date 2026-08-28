# ADR-081: A recovered panic value is reported by type, never by value

- **Status**: Accepted
- **Date**: 2026-08-22
- **Related**: [ADR-083](adr_083_span_sinks_record_errors_by_type.md) (generalizes this rule to every error a framework span sink records) · [ADR-079](adr_079_log_filter_walks_slices_without_comparing.md) (guarded these reporting calls and stated the protection this ADR corrects) · [ADR-072](adr_072_default_log_filter_names_key_material_explicitly.md) (the field-name matcher whose reach is the whole issue) · [ADR-019](adr_019_migration_audit_delivery.md)

> **Amended (2026-08-28, the site outside Recover):** this ADR's rule was applied
> wherever the framework RECOVERED a panic, and one HTTP path recovered none.
> Echo v5 has no top-level recover, and seven middlewares — request id, OTel,
> request enrich, CORS, IP pre-guard, tenant resolution, forwarded client cert,
> and the access logger — are registered OUTSIDE `Recover`, so a panic in any of
> them unwound past Echo into net/http, which prints `http: panic serving <addr>:
> <value>` with a stack to the standard logger and drops the connection. The
> value was therefore rendered by a sink this framework does not own, on a path
> where the caller saw only EOF and no access-log line was written.
> `http.Server.ErrorLog` is not the fix: net/http formats the value into the
> string before any adapter sees it.
>
> An outermost guard is now registered as the FIRST middleware, so every other
> middleware runs inside it. It applies this ADR's rule unchanged — one ERROR
> line naming the panic's `%T` alongside the method and path, never the value —
> and answers with the standard 500 envelope. `http.ErrAbortHandler` is
> re-panicked by identity, not `errors.Is`, for the reason `sanitizePanicValue`
> already states: a bypass gate that matches a WRAPPED sentinel would hand the
> wrapper's payload to net/http's own renderer. The guard does not touch the
> span — a pre-`Recover` panic ends its span without an error status, which is
> accepted rather than reaching around the OTel middleware from outside it — and
> the existing `Recover` + `sanitizePanicValue` pair is unchanged, so panics
> downstream of it behave exactly as before (#1144, `[C61.12]`).

## Context

ADR-079 guarded two panic-reporting calls so that a failure to render the
recovered value could not escape an already-spent `recover()`. It also wrote,
of the primary path, that the value "goes through the sensitive-data filter and
is masked by field name exactly as any other logged value."

That is true only for some values. The filter matches **field names**, and the
field here is `panic`, which is not a needle. What reaches the sink therefore
depends on the shape of whatever the panicking code chose to panic with —
measured against the default needle list:

| `panic(...)` value | emitted |
| --- | --- |
| `"secret-string"` | `"secret-string"` — in clear |
| `map[string]any{"password": "pw"}` | `{"password":"***"}` |
| `map[string]any{"licenseKey": "pw"}` | `{"licenseKey":"pw"}` — in clear |
| `struct{ Token, Host string }` | `{"Host":"h","Token":"***"}` |

A bare string has no inner field name to match at all. A map key the needle list
does not name is emitted verbatim. So the protection was **conditional on a shape
the framework cannot constrain**, described as general.

The framework's own code already knew this. `audit_emitter.go` carried a
`// SECURITY:` comment reading *"a panic carrying a secret (`panic(cfg)`) would
reach the sink in clear"* — but attached to the fallback path, while the primary
was documented as safe.

Three sinks receive a recovered value, and only one of them consults the filter
at all: the log field via `Interface`, `span.RecordError`, which ships it
off-platform to the tracing backend, and the scheduler summary line's `Err()`.
ADR-079 already made the latter two type-only for exactly this reason; the
inconsistency was that the third was not.

## Decision

**Every FRAMEWORK report of a recovered panic value names its TYPE and never the
value**, across eight sites in six packages. The rule covers three kinds of site,
and only the first is what a search for logging calls finds:

1. **Four report the value directly** to a log field — `migration/audit_emitter.go`,
   `scheduler/module.go`, and `messaging/internal/delivery`'s `AppendOutcome` and
   `settleOnce`.
2. **Three RENDER it into an error** that later reaches a sink — `delivery.go:38`'s
   shared `panicMessage` (used by both `invoke` and `panickedResult`),
   `multitenant/cleanup.go:61` and `messaging/manager.go:213`.
3. **One replaces the value before a third party can render it at all** —
   `server`'s `sanitizePanicValue`, covering the HTTP lane.

**The HTTP lane IS covered**; an earlier draft of this ADR called it "the one path
this does not reach", which was true when written and is now exactly the kind of
sentence that licenses a reader to stop auditing. Echo's `Recover` still runs its
own `fmt.Errorf("%v", r)`, and that is harmless here precisely because the value it
receives has already been replaced by its type — the `%v` renders `panic (type: T)`.
`AppendOutcome` is the delivery SPINE, shared by the classic AMQP lane and the
streams lane, so the rename lands on every consumer whose message handler panics,
not only on those running scheduled jobs or an `AuditRecorder`. The stack trace is
retained wherever it was emitted, so the panic site is still identified;
`audit_type` and `target` still make a dropped audit event attributable, and the
scheduler still records the job as failed with its `jobID`.

This removes the leak class rather than documenting it. The alternative —
value-aware redaction — would have to decide, for an arbitrary value chosen by
consumer code, which parts are sensitive. That is the document-shape problem
already deferred in ADR-079, and it is a poor trade here: a panic value's
diagnostic worth is mostly its type and its stack, both of which survive.

**The audit emitter's two-tier report collapses to one.** Its fallback existed
solely because rendering the value could panic; nothing renders the value now, so
the fallback duplicated the primary. What remains is a single type-only report
wrapped in the terminal swallow, which is still earned — the logger itself is
consumer-supplied and can panic.

**The sites are made to match deliberately.** A job's panic value is exactly
as consumer-controlled as an audit sink's, so a divergence would need a reason
and there is none. Before this change the scheduler contradicted itself inside one
function: its summary line already read `panic (type: string)` while the log field
one line above emitted the value in clear.

## Consequences

- **Log and span content changes on every surface `[C60.23]` enumerates**, which this
  repo treats as breaking (precedent: `[C60.7]`, `[C60.13]`). The `panic` field becomes
  `panic_type` on the audit sink-failure line, the scheduler job-panic line, the
  delivery settle line, and the shared delivery outcome line on both messaging
  lanes. The HTTP surfaces carry no `panic` field at all and change anyway (see the
  `appendErrorDetail` consequence below). The migration atom owns the surface-by-surface
  table and is the one place to maintain it — the eight sites counted in the Decision are
  where the RULE WAS APPLIED, which is a different question and a different number. The delivery spine's key set is itself asserted by
  `messaging/internal/lanecontract`, so a consumer with an equivalent lane-shape
  test sees the same break. A saved
  query, alert or log-parsing test matching the old field or its value stops
  matching — **silently**, which is the failure mode worth repointing for.
- **Span content changes too, and separately from the log.** Both messaging lanes'
  `exception.message` and span status description move from
  `panic in message handler: <value>` to `panic in message handler (type: T)`, and
  the scheduler's cleanup-job error does the same. An alert keyed on the span rather
  than the log sees a break the log-field rename does not describe — which is why
  `[C60.23]` documents the two separately.
- The audit line's second message, `"...(value unrenderable)"`, is gone. It was
  reachable only when rendering the value panicked, which no longer happens.
- Diagnostics lose the panic value on every path that can reach a sink — log field,
  span exception, span status and returned error alike. Where that disclosure
  previously depended on the value's shape, it is now impossible.

- **One of those sinks is closed by a provider option, not by the call sites, and
  the distinction matters to anyone reasoning about a site in isolation.** The OTel
  SDK's own `span.End()` calls `recover()` and stamps
  `semconv.ExceptionMessage(fmt.Sprint(recovered))` before re-raising, so any
  `defer span.End()` unwinding with a live panic shipped the value off-platform
  regardless of what the surrounding code spelled. Six framework sites do that, and
  **four of them have no first-party `recover()` at all** — `server/jose.go`'s two,
  `messaging/amqp_client.go` and `messaging/streams/publisher.go` — so no convention
  about how to write a recovery site could ever have reached them.
  `observability/provider.go` now passes `sdktrace.WithoutPanicRecording()`.

  `messaging/internal/delivery`'s `Run` was exposed by its own deliberate ordering:
  `defer span.End()` is registered last and therefore runs FIRST, ahead of the
  recover. A handler panic never reaches it — `invoke` catches that one — but a
  panic in the delivery TAIL does, which is the case `panickedResult` exists for and
  the one ADR-079 saw in the wild when the log filter panicked inside `LogOutcome`.
  Reordering was rejected: it fixes one site, argues with a comment that exists to
  explain why the order is what it is, and leaves the other four open.

  **The cost, stated rather than left implicit:** an unwinding panic no longer
  produces an exception event on the span at all. An operator watching for those
  will stop seeing them, and that is a silent change — `[C60.23]` carries it. It is
  accepted on the ADR's own reasoning: a panic's diagnostic worth is its type and
  its stack, and the framework's own reporting retains both.

- **The HTTP lane was the largest instance of this ADR's own rule, and it is
  fixed here rather than carved out.** Echo's `middleware.Recover` builds its
  error with its own `fmt.Errorf("%v", r)`, and the OTel middleware is registered
  OUTER to it, so it read that error and put it in the span's status description —
  off-platform, on every panicking request, **in production posture and ungated by
  `App.Debug`**. The request logger sits outside Recover too and stamped the same
  error on the action line. One root cause, two renderers; a reader who sees only
  the span fix would otherwise assume the log path was handled separately.

  `sanitizePanicValue` is registered immediately INSIDE Recover, recovers the raw
  value, and re-panics with an error naming only its type. That placement is the
  whole design: by the time Recover produces a `PanicStackError` it has already
  wrapped a non-error panic in `fmt.Errorf`, so reading the type there reports
  `*errors.errorString` for every string panic — type-safe and diagnostically
  worthless, which would gut the trade this ADR makes. Typing the RAW value keeps
  `string`, `map[string]string` and a consumer's own `*svc.DomainError` intact.
  Echo's Recover then adopts the sanitized error verbatim (`tmpErr, ok := r.(error)`),
  so its stack capture, its `PanicStackError` shape and the `errors.As` in the HTTP
  error handler all keep working by construction rather than by reimplementation.
  A value that is ALREADY an error gets the same treatment — Echo adopts those with
  no rendering at all, so `panic(fmt.Errorf("secret=%s", s))` would otherwise pass
  through untouched.

  Two properties are pinned by test because both would fail silently: that Echo
  still adopts the error verbatim (asserted on the unwrapped TYPE, since the
  message text reads identically either way), and that the re-panic leaves the
  original panicking frame in the captured stack. `http.ErrAbortHandler` is
  re-panicked unchanged, preserving `net/http`'s abort contract. Recover now runs
  with `DisableStackAll: true`: `PanicStackError.Error()` concatenates the stack
  into that span attribute on every 500, and the default dumps every goroutine.

  This closes [#1138](https://github.com/gaborage/go-bricks/issues/1138), which
  described the request-log half before the span half was known.

  `observability.FrameworkTracerProviderOptions()` is exported so a consumer
  building its own `TracerProvider` in a test reproduces production policy. Without
  it a test asserting "no panic value reaches a span" passes or fails for reasons
  unrelated to the code under test — the framework's own test seam had exactly that
  divergence until this change.

- **A neighbouring policy disagrees, deliberately.** `server/server.go`'s
  `appendErrorDetail` answers the same question differently: it emits `err.Error()`
  when `App.Debug` is set and `error_type` otherwise, and its doc comment cites the
  same premise (the filter masks by field name, not message content). Panic values
  get NO such escape hatch here, and the difference is intended — a driver or
  handler error is text the framework or the consumer's own code composed, while a
  panic value is an arbitrary object chosen by code that was, by definition, not
  working correctly at the time. A reader comparing the two should find that stated
  rather than infer an inconsistency.

  **That line's own output changes here even though its code does not.** `server/server.go`
  is untouched by this decision, but both inputs to its `Panic recovered` line move:
  `sanitizePanicValue` makes `panicErr.Unwrap()` a `*panicTypeError` instead of the value's
  own error type — `*errors.errorString` where Echo's `fmt.Errorf("%v", r)` rendered a
  non-error, the concrete type where the code panicked with an error — so `error_type` reads
  the CONSTANT `*server.panicTypeError` in production posture and the `App.Debug` `error`
  field stops carrying the value; and `DisableStackAll: true` shrinks that line's `stack` to the
  panicking goroutine. The same is true of the `unhandled error` line below it, whose
  `app.debug` rendering carries the value — its production `error_type` is the wrapper
  `*middleware.PanicStackError` and does not move. These are reporting surfaces the rule
  reaches without editing them.

- **The rendered type name is a consumer-visible string, and no gate protects it.**
  `server/middleware.go`'s `panicTypeError` is unexported, so `apidiff` cannot see it, yet
  `[C60.23]` instructs operators to repoint dashboards on the literal `*server.panicTypeError`.
  Renaming that type is free by every gate this repo runs and would silently break every
  repointed dashboard — exactly the failure class this decision exists to prevent. Rename it
  only with a migration atom.

- **Getting to an unqualified framework-side claim took three more sites.** An
  earlier draft asserted it while three live paths falsified it. All three shared
  one shape — `recover()`'s value rendered with `%v` into an `error` — which is why
  no search for a logging call found them. All three are fixed here:
  - `messaging/internal/delivery`'s shared `panicMessage` (`delivery.go:38`), which
    both `invoke` and `panickedResult` use, so the value reached `span.RecordError`
    and `span.SetStatus` — `exception.message` verbatim, off-platform. One constant,
    both sites.
  - `multitenant/cleanup.go:61`, a consumer `RetentionDelete` callback's panic
    rendered into an error one frame BELOW the scheduler. Traced end to end:
    `FanOutRetentionCleanup` joins it, the outbox and inbox cleanup jobs return it
    from `Execute`, and the scheduler treats it as an ordinary job error —
    `span.RecordError` (`scheduler/module.go:742`), `span.SetStatus` (`:743`) and the
    summary line's `Err(err)` (`:822`). Three sinks, log included.
  - `messaging/manager.go:213`, a panic during consumer setup. Lower exposure — the
    panic originates in framework and broker-client code, not a consumer callback —
    but a per-tenant broker configuration is exactly where a credential lives, and
    an incomplete enumeration is the same defect as the absolute, one size down.

- **The rule binds at the point of CONVERSION, not only at reporting sites.** The
  `multitenant` path is the instructive one: the type-only discipline was installed
  on the scheduler's PANIC path (`scheduler/module.go:681-713`), and converting a
  panic into an error one frame lower routed the value down the ERROR path instead,
  where every sink handles it as a normal error and prints it. **A guard on the
  panic path cannot protect a value that stops being a panic before it arrives.**
  Wherever `recover()`'s result becomes an error, `%T` applies there too.

  This is not a new rule so much as one the tree already had and applied unevenly:
  `httpclient/client.go:750,812` has carried it since its own `// SECURITY:`
  comments, and its shape is the model — the TYPE goes into the span error, the raw
  value is re-panicked so the caller's own `recover` can decide what to do with it.

- **Why the false absolute survived review, which is subtler than a careless grep.**
  The verification reused ADR-079's sweep, `git grep 'Interface("panic"'`, together
  with ADR-079's argument for why that sweep is complete: *every other recover site
  either does not render the value or renders it through `fmt.Errorf("%v", r)`,
  which is panic-safe.* **That argument is sound — for ADR-079's question.** ADR-079
  asked which sites can PANIC while reporting, and `%v` sites genuinely are outside
  that class. This ADR asks which sites can DISCLOSE, and for that question the set
  ADR-079 explicitly excluded is precisely the set that answers it: `%v` is
  panic-safe *and* value-printing. The sweep was inherited across a change of
  question, and its completeness argument did not survive the trip.

  The general form: **a search is only complete for the question its class boundary
  was drawn around.** Reusing a prior sweep requires re-deriving that boundary, not
  just re-running the command. For disclosure the boundary is every renderer that
  can reach a sink — `Interface`, `%v`/`%+v`/`%s` into an error or a message,
  `Err()`, `span.RecordError`, `span.SetStatus` — not one call spelling. ADR-079's
  sweep paragraph now carries a warning against reusing it the way this ADR did.

- **The property is pinned by tests now, which is the part that was missing.** A
  claim no test defends is a claim that decays silently: `TestRunKeepsAPanicValueOutOfTheSpan`
  panics a handler with a secret-shaped literal and asserts it appears in neither the
  span status description nor any exception attribute, and the `multitenant` and
  `manager` paths carry the same assertion. Each was confirmed load-bearing by
  reverting its source line and observing the red.

- **The guard enforcing this rule was itself written shape-conditional three times,
  which is worth more than the fix.** `TestAppendOutcomeNeverDisclosesThePanicValue`
  walks `Result`'s fields looking for the panic value. Three separate renderings
  were proposed for that walk and two of them were necessary but not sufficient in
  the SAME way — the guard stops crashing, or looks thorough, while staying blind to
  one shape. `%v` misses a `[]byte` (renders `[110 111 …]`) and a pointer (renders an
  address). `%#v` fixes the struct case and still misses the pointer. Falling byte
  ARRAYS through to an element walk avoids `reflect.Value.Bytes`'s "unaddressable
  byte array" panic and then finds nothing, because each byte renders `0x6e`
  separately — **a version that passes a test which ought to fail.**

  So the weakness this ADR condemns in the field-name filter — protection
  conditional on the shape of a value the framework cannot constrain — was
  reintroduced three times inside the machinery built to prevent it, by three
  different authors. **Nothing but a probe per shape caught any of them.** Reading
  the renderer did not, in any of the three cases; each looked correct on the page.
  A guard for a shape-independent property has to be tested against the shapes, one
  at a time, with the field actually re-added and the failure observed.

- **One standing hazard, deliberately not fixed.** gocron's executor renders a
  recovered job panic with `%v`. It reaches no sink today because the framework
  wires no `WithLogger` and no event listeners, so the rendered value is discarded
  — but it is one `gocron.WithLogger` call away from being live, and that call
  would look entirely innocuous. Recorded so the next person to add scheduler
  logging knows it is a decision, not a detail.

- ADR-079's claim about the primary path is corrected in place rather than left
  standing with a superseding note, because a reader checking whether they are
  exposed should not have to find two documents to get one answer.

One cost of the terminal swallow is worth stating, because it was paid during
this change: a recover-all guard makes its own package harder to debug. Adding
`Str` to the report meant a test double missing that method hit a nil embedded
interface — and the swallow ate the resulting panic, so the test failed with an
empty capture rather than a stack. The guard was doing exactly its job and hiding
a test defect at the same moment. That is the trade for "nothing left to report
with"; it is accepted, not overlooked.

Migration: [C60.23](migrations.md).

## Alternatives considered

**Keep `Interface("panic", r)` and rely on the filter.** The status quo, and the
measurements above are the argument against it: protection varies with the value's
shape, and the two shapes it misses — a bare string and an unnamed map key — are
ordinary things to panic with.

**Value-aware redaction.** Needs a rule for arbitrary consumer values; a heuristic
that redacts anything secret-looking would mangle legitimate diagnostics, and one
that parses known document shapes is the deferred ADR from ADR-079. Neither buys
much when the type and stack are what a reader of a panic report actually uses.

**Type-only at the audit emitter, value at the scheduler.** Rejected explicitly.
The argument for withholding is identical at both, the scheduler already withheld
it on two of its three sinks, and a per-site rule is one an author has to
rediscover each time.

## Addendum (2026-08-23): the response-body sink shares the debug gate

This ADR reasoned about the sinks that RENDER a recovered panic — the log line,
the span status, the audit record — and left one out: the HTTP response body.
`classifyError` attached the recovered error's text to `details.error` under
`cfg.App.IsDevelopment()` alone, while the two log paths withheld the same text
under `cfg.App.Debug`. The type-only rule held on both — `sanitizePanicValue`
runs a frame lower, so what the body carried was already `panic (type: T)` and
never the value — but the GATES disagreed, and they disagreed in the direction
that costs the most: an operator who turned `app.debug` off while `app.env`
stayed a development alias silenced the copy they read and kept shipping the
copy the caller reads, which is the less trusted of the two audiences.

The body now requires both keys, `cfg.App.Debug && cfg.App.IsDevelopment()`, so
the stricter of the two always wins and the two sinks cannot diverge. The log
paths are unchanged, and the debug rendering keeps carrying the type and the
stack — the documented debug opt-in this ADR already accepts. The rule this adds
is narrower than "report by type": **a sink's gate is a claim about who may read
it, so two sinks reporting the same thing must not be gated on different keys.**
The one that reaches the least trusted audience takes the stricter gate.

Migration: [C60.30](migrations.md) · issue #1140.
