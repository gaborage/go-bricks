# ADR-079: The log filter decides slice passthrough by type, and panic reporting cannot panic

- **Status**: Accepted
- **Date**: 2026-08-21
- **Related**: [ADR-072](adr_072_default_log_filter_names_key_material_explicitly.md) (removing the bare `key` needle is what routes a JWKS into the walker; its JWKS consequence paragraph is amended in an addendum there) · [ADR-019](adr_019_migration_audit_delivery.md) (the "sink errors log but don't abort" guarantee this restores for panics)

## Context

Three defects, one reachable from any HTTP or broker payload.

### 1. The walker panicked on a JSON array of objects

`filterSliceOrArrayWithProtection` built a filtered copy of every slice and then
asked whether anything had changed, so that an unchanged slice could be returned
with its concrete type intact:

```go
filteredElem = f.filterValueWithProtection(key, elem, visited, maxDepth-1)
if filteredElem != elem {
```

Both sides are `any`. Comparing two `any` values panics when their dynamic type
is uncomparable, and `isStructType(elemVal.Type())` — the branch that would have
avoided the comparison — sees `interface{}` for an element of a `[]any` and is
never taken. So the guard existed and did not fire.

Reproduced against `aa971558` in a throwaway module:

| Input | Result |
| --- | --- |
| `{"data":[{…}]}` — any list envelope | **PANIC**: `comparing uncomparable type map[string]interface {}` |
| `{"keys":[{…}]}` — a JWKS | **PANIC** |
| `{"items":[[1],[2]]}` — nested arrays | **PANIC** |
| array of scalars | ok |
| single object at root | ok |

This is not JWKS-specific. `[]any` of `map[string]any` is the shape
`encoding/json` produces for every JSON list of objects, so **any** body shaped
`{"…":[{…}]}` crashed the log path. The existing filter tests missed the entire
class because they feed **typed structs**, which take the struct branch; real
decoded JSON never does. Present since #43 (2025-09-22), reached in practice
once ADR-072 stopped the walk being cut short at a `keys` field.

**Both doors reach it** and both had to be covered:
`.Interface()` on any log event → `FilterValue`, and
`Logger.WithFields(map[string]any)` → `FilterFields` → `FilterValue` per entry.

Blast radius at the framework's own call sites:

| Path | Guarded by | Outcome |
| --- | --- | --- |
| `httpclient` `Interface("body_preview", m)` under `LogPayloads` | echo `Recover` / scheduler recovery / messaging nack-on-panic | lost log plus a failed request, job, or nacked message — request-scoped denial of service |
| `scheduler` `Interface("panic", r)` in the FR-021 recovery defer | gocron's `callJobWithRecover` | no process crash, but see 3 |
| `migration/audit_emitter` `Interface("panic", r)` in `deliverToSink`'s recovery defer | **nothing** | **process crash** |
| `messaging/internal/delivery` `AppendOutcome` stamping a handler's recovered `Panic` | `Run`'s outer guard | contained, and misleading: the guard recovers the WALKER's panic and `panickedResult` settles on that, so the delivery's outcome line is lost and the record names `comparing uncomparable type …` instead of what the handler actually panicked with |

### 2. One empty needle masked the entire log stream

`strings.Contains(x, "")` is true for every `x`. A single empty needle therefore
matched every field name, and the filter replaced the whole log stream with the
mask value — framework identity fields included, verified as
`"app":"***","env":"***","version":"***"`.

`app.resolveLoggerFilterConfig` trimmed, dropped empties and de-duplicated, but
only on the `log.sensitivefields` route. `app.Options.LoggerFilterConfig`
**replaces** the whole config and reached the matcher un-normalized, so the
code-level door — the one documented for composing needles from a secret manager
at startup — was the unguarded one. The startup WARN did not cover it either: it
fired only when `SensitiveFields` was *fully* empty.

### 3. Reporting a panic could panic, and that escaped an exhausted recover

Both recovery handlers log the recovered value with `Interface("panic", r)`.
`r` is supplied by consumer code and rendering it is not guaranteed to succeed —
by defect 1 for a slice-bearing value, and in general for any value whose
encoding panics. The handler is a defer that has **already** called `recover()`,
so a panic there propagates.

At `migration/audit_emitter.go` that defeats a guarantee the framework already
shipped. #686 added `deliverToSink`'s recover precisely so that *"a faulty
`AuditRecorder` cannot crash a migration mid-run (ADR-019: sink errors log but
don't abort — panics must behave the same)"*. But the escape route runs
`deliverToSink` → `consumeSink`, which has no guard of its own and runs as a bare
`go e.consumeSink(consumerCtx)` — a goroutine panic, which is the process.
Confirmed by execution: the escape is attributed `audit_emitter.go:303` →
`:311` → `consumeSink:280` → the `go` statement at `:172`, and it killed the test
binary.

At `scheduler/module.go` gocron converts the escape to `ErrPanicRecovered`, so
there is no crash — but the reporting call is the **first** statement of the
recovery block, and everything that makes the failure visible comes after it:
`entry.metadata.incrementFailed()`, the span error, the metrics, and the
structured summary. An escape there leaves a panicking job counted as neither
success nor failure.

So item 3 is two defects, not one. The crash is the loud half; the quiet half is
that an escape **skips the rest of the recovery block**, and at the scheduler that
block is where the outcome is recorded. A panicking job ends up counted as
neither success nor failure, and nothing says so.

`messaging/internal/delivery/delivery.go` already defends this exact pattern with
a nested `defer func(){ _ = recover() }()` and the comment *"if logging the panic
panics too, there is nothing left to report it with."* That defense existed at
one of three panic-reporting sites — and it is order-safe only incidentally: its
log call happens to be the last statement in the handler, so a whole-handler
guard loses nothing there. Nothing marks that as a requirement, and neither of
the two sites fixed here satisfies it. That site deliberately keeps the older
shape in this change: converting it is behaviour-identical, so no test can go red
on it, and it would put changed lines in a fourth package for a latent hazard.
Moving all three to the narrow shape is tracked as [#1134](https://github.com/gaborage/go-bricks/issues/1134).

The class was swept rather than assumed: `git grep -nE 'Interface\("panic"'`
over non-test Go returns four hits. Three are the class — a recovered value
logged from a defer that has already spent its `recover()`: `delivery.go:303`
(already guarded, the precedent), `audit_emitter.go` and `scheduler/module.go`
(both guarded here). The fourth, `delivery.go`'s `AppendOutcome`, is an ordinary
helper on the lane's outcome line, not a defer, and `Run`'s outer guard catches
it deliberately — `Run` installs its recover at `delivery.go:190-191`, before
`req.LogOutcome` runs at `:215`, and converts a panic there into a
`panickedResult` that is still settled. So: three sites in the class, all three
addressed.

The sweep is complete rather than lucky, and the reason is worth stating: every
OTHER recover site in the tree either does not render the recovered value at all
or renders it through `fmt.Errorf("%v", r)`, and `fmt` catches panics in
`String()`/`Error()` and emits `%!v(PANIC=…)` instead of propagating.
`LogEvent.Interface` is the only renderer in the tree that is not panic-safe,
which is why grepping for it finds the whole class.

## Decision

**The walker decides passthrough-versus-copy from the element TYPE, before
descending.** A slice whose element type the walker cannot rewrite is returned
as-is; anything else is rebuilt as `[]any`. No two values are compared, so the
panic has no site to occur at. `rewritesType` answers true for interface, struct,
map, slice, array and pointer kinds — interface because an element's concrete
type is only known per value, pointer conservatively, since rebuilding a slice
loses nothing but its concrete type. Depth is part of the same decision: at
`maxDepth` 1 the elements are masked, and a mask is a rewrite whatever the
element type says.

Deciding before the loop rather than after it also removes the work the old form
did in order to answer the question. The old code boxed every element with
`elemVal.Interface()` and populated a `[]any` it then discarded whenever nothing
had changed; the early return skips both. Measured on a 100-element `[]string`:
3517 ns / 3416 B / **102 allocations** before, 31.8 ns / 24 B / **1 allocation**
after — the one remaining allocation being the slice header the old code also
returned. That is a side effect, not the motivation, and it is worth stating only
because it disposes of the obvious objection that a type check per slice costs
something.

The plan offered a second form — drop the comparison and always return the
filtered slice, which has one fewer decision site. It was implemented first and
rejected on evidence: it is **not** behaviour-preserving. Four existing test
functions fail under it — `[]string` and `[3]int` stop surviving as themselves —
and, worse than the pins, `[]byte` would stop logging as base64 and start logging
as an array of numbers. The comparison was buying something real; only its mechanism
was wrong.

**Needle normalization moves to `NewSensitiveDataFilter`**, where a list becomes
a filter. Trim, drop-empty and de-duplicate happen once, at the seam every
construction door passes through — so `app.Options.LoggerFilterConfig` gets the
rule it was bypassing, and `app.resolveLoggerFilterConfig`'s loop is deleted
rather than duplicated. The `anyEmpty` field and its branch in `isSensitiveField`
are removed: an empty needle can no longer reach the matcher, so the branch was
unreachable.

Dropping empties creates one new way to be silent, and it is closed in the same
change: `SensitiveFields: [""]` normalizes to zero needles, which masks nothing.
The startup WARN now judges the **effective** needle list rather than the raw
slice length, so a list that normalizes away announces itself exactly as an empty
one does.

**The panic-reporting call is isolated at both sites.** The guard wraps the
reporting call alone — not the handler:

```go
func() {
    defer func() { _ = recover() }()
    // … the reporting log call …
}()
```

Wrapping the handler — the shape the `delivery.go` precedent suggests — was
implemented first and is wrong. It contains the crash and **keeps the accounting
loss**: the unwind still skips `incrementFailed`, the span error, the metrics and
the summary, so the job is still counted as neither success nor failure. Only the
narrow guard fixes both halves. It also makes neither site depend on the log call
happening to be last, which is the only reason the precedent is safe where it
stands.

The fallback reports the panic's **type**, never its value. The primary call
renders the value through this very filter, which masks by field name; the
fallback can only use `Str`, which masks on the KEY — so `%v`-ing the value there
would emit a secret the primary path would have masked, into a field no needle
reaches. An audit sink panicking with its own config (`panic(cfg)`) is exactly
the shape that costs. `httpclient`'s `Do` recovery already carries this rule and
its reasoning; the type plus `audit_type` and `target` is all the line needs to
make a dropped event attributable.

The same rule is applied to the scheduler's `panicErr`, one line above its
guard, because ADR text that says "type, never the value" while a sibling line
renders the value is not a rule. `panicErr` fed two sinks the filter does not
touch, and the worse one leaves the platform: `span.RecordError` ships the value
to the **tracing backend** as an exception event — a third-party system with its
own retention, access model and export path — and the summary line's `Err()` is
the second. `httpclient`'s `Do` recovery already names OTel exception events in
its own `// SECURITY:` comment, so this is an established rule here rather than a
fresh judgement. Both carried the panic value in clear
while the reporting call immediately above them emitted the SAME value masked —
observable in one capture as `"panic":{"jobRef":"nightly-sync","password":"***"}`
followed by `"error":"panic: {nightly-sync test_password_123}"`. The summary line
and the span now name the type; the value keeps its one filtered route.

The broader gap this sits in is NOT closed here, and it spans **both** sinks:
`LogEvent.Err` applies no filtering at all, and neither does `span.RecordError`.
Closing one door leaves the other open, and the open one would be the
off-platform half — so the follow-up has to ask how many `RecordError` call sites
pass a consumer-controlled value, not only how many `Err` calls do. It is an
unmade decision rather than an oversight: field-name masking cannot help when the
key is `error`, so closing it needs value-shaped redaction, the same family as
`redactURLForLog` and the deferred document-shape work. The framework handles it
per-site today (migration's password redaction, the database error scrubbing).
Filed as [#1132](https://github.com/gaborage/go-bricks/issues/1132), scoped to both
sinks.

One compensating control is worth naming because it is level-gated: the
masking-disabled WARN is emitted on the already-level-filtered logger, so at
`log.level: error` and above a config whose needles all normalize away boots
silent with no masking. That is the same suppression the WARN always had, but it
now guards a posture that used to announce itself by masking everything.

`consumeSink` itself is deliberately left unguarded. Adding a recover there
changes the audit emitter's failure model — a panicking sink currently kills the
process, and after such a change would not — which is a decision of its own, not
a side effect of this one.

## Consequences

- Any log of a JSON body shaped `{"…":[{…}]}` stops crashing the log path. For
  `deliverToSink` that is the difference between a dropped audit event and a
  dead process; for the HTTP and scheduler paths it is the difference between a
  lost log line and a failed request or job.
- **Consumer-visible, and why this is a breaking change:** a slice whose elements
  the walker rewrites is now emitted as `[]any`. Serialized output is unchanged —
  this is observable only to a consumer whose code depends on the CONCRETE TYPE of
  the public `FilterValue`'s result — a type assertion, a type switch, reflection,
  or any branch keyed on the type. The type switch is the quiet one: its arm stops
  being selected and falls through to `default` rather than failing. A typed **nil** slice is the one case where rebuilding WOULD have
  been wire-visible (`[]` where the line carried `null`), so it is preserved
  explicitly, matching the two typed-nil map guards the walker already had. Slices
  of SCALARS and `[]byte` keep their concrete type; a non-nil slice of typed
  STRUCTS does not — `rewritesType` answers true for `reflect.Struct`, so
  `[]MyStruct` is rebuilt and emitted as `[]any`, with the serialized output
  identical (`[{"name":"john"}]`). Measured, not inferred. What is preserved for
  struct slices is the output, not the type. There is no longer any input for
  which the emitted type depends on whether a needle happened to match.
- **Consumer-visible:** a needle list containing an empty, whitespace-only or
  duplicate entry is normalized rather than taken literally. A deployment that
  was masking everything because of a stray empty entry starts logging its
  non-sensitive fields again — which is the intent, but it is a change in what
  reaches the log sink. A `FilterConfig` whose needles all normalize away now
  emits the masking-disabled WARN.
- `app.resolveLoggerFilterConfig` only merges now. Its returned
  `SensitiveFields` can contain entries that normalization will later drop; the
  effective list lives behind the filter.
- The two guards restore #686's shipped guarantee rather than adding new defense
  in depth. The walker fix alone would have closed today's reachable route and
  left the general one open: any consumer value whose encoding panics.

Deliberately **not** done here, each because it is a different decision:

- **Document-shape recognition** (JWK/JWKS/PEM/JWT), so a JWK's `d` — the RSA
  private exponent — stops logging in clear. That is masking by *position and
  neighbours* rather than by name, which no needle list expresses at any length.
  It changes what every consumer masks and needs its own ADR. ADR-072 documents
  the leak and that documentation stands.
- **`consumeSink` recovery semantics**, above.
- **The four dead needles** — `access_token`/`refresh_token` under `token`,
  `authorization` under `auth`, `credentials` under `credential` — which break
  ADR-072's own containment rule.
- **Breadth bounding.** Depth is capped at 8; breadth is not, on a remote
  server's response body.
- **A known confidentiality gap in a shipped release** — deferred deliberately,
  not overlooked. Pre-encoded payloads, and the one place this change trades a
  crash for a leak.
  `json.RawMessage` is a `[]byte`, so the walker passes it through and its
  contents are never masked. For a value at the top of a field that is unchanged
  and pre-existing: `Interface("body", json.RawMessage(...))` logged
  `{"password":"pw"}` in clear before this change and still does.

  A `[]json.RawMessage` is different, and the difference should be stated
  plainly rather than filed as "a latent gap became visible". Its elements are
  themselves uncomparable, so it hit the same panic this ADR fixes. **Before,
  that shape crashed the log path — contained by whichever caller was recovering.
  After, it renders `[{"password":"pw"}]` unmasked.** A crash traded for a silent
  leak. The crash was at least loud.

  What narrows the exposed population is not the door but the starting state:
  **anyone hitting this shape today is already crashing.** No deployment is
  quietly logging `[]json.RawMessage` successfully right now and becomes leaky on
  upgrade — the affected code is a path that was already failing (a rare error
  branch that logs a raw payload) or code written after this change. That is the
  difference between a regression and a newly-reachable gap, and it is why this
  ships disclosed rather than blocking the panic fix.

  The door itself is the ordinary public log API — verified through both
  `.Interface()` and `WithFields` — not only `httpclient`'s dev-only
  `LogPayloads`. What bounds it is that the value has to be a
  `json.RawMessage` the caller chose to log: the framework logs none itself, so
  reaching this requires consumer code that logs pre-encoded JSON containing a
  secret. There is no cheaper remedy than the deferred one: this filter
  masks by FIELD NAME, and the secret here is a key *inside* opaque bytes under an
  innocuous field like `body`. No needle finds it without parsing, and a heuristic
  masking any raw payload whose bytes merely contain a sensitive-looking key would
  mask legitimate payloads on a substring coincidence. Parsing is the
  document-shape decision deferred above, tracked as
  [#1133](https://github.com/gaborage/go-bricks/issues/1133); re-encoding a
  consumer's byte-exact payload is its own decision, since byte-exactness may be
  why it is a `RawMessage` at all.

  `**T` to a struct is never followed either (the pointer arm requires
  `Elem().Kind() == Struct`). That one is unchanged in both directions.

Migration: [C60.21](migrations.md).

## Alternatives considered

**Type-switch the element and skip the comparison only for uncomparable kinds.**
Keeps a comparison for the kinds where it is safe, and keeps the reader asking
which those are. The set of uncomparable dynamic types is exactly the set the
walker rewrites, so asking "does the walker rewrite this type" answers both
questions at once and leaves nothing to get wrong later.

**Guard `LogEventAdapter.Interface` instead.** The one seam every `Interface()`
call passes through — the framework's and every consumer's — so one `recover`
there would cover the walker's own reflection, zerolog's marshal, and every
future site at once. Rejected because it changes the global failure model rather
than fixing a bug: an encoding panic would become silent on the ordinary happy
path, not just on the already-panicking one, and it adds a `defer` to a path that
carries allocation guards. The narrow guards apply only where a panic is already
in flight and nothing else can report it.

**Recover inside `filterSliceOrArrayWithProtection`.** Turns the panic into a
mask or a passthrough and leaves the bug — a filter that cannot walk a JSON array
is broken whether or not it announces it, and the recovered path would have
silently emitted un-filtered or over-masked data depending on which side of the
recover it landed.

**Fix the needle list in `app` only.** That is where the loop already was, and it
is exactly why the replace-door was unguarded: `app.Options.LoggerFilterConfig`
does not pass through it. A rule that lives at one of two doors is a rule that
the other door will keep breaking.

**Guard `consumeSink` instead of the reporting call.** It stops the crash and
hides the cause: the sink's panic would be reported by a handler that then dies
un-noticed, and the audit emitter's documented failure model would change
silently. The reporting call is where the second panic happens, and it is where
the guard belongs.
