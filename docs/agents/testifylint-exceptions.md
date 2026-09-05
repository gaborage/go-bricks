# testifylint exceptions

Package PRs in the #1092 wave APPEND their deliberate non-conversions here as they land; the
FINAL PR (`ci(lint): enable the remaining testifylint checkers`) converts every row into the
`//nolint` directive its last column names, then drops this file's pending status. Directives
cannot be added earlier — while a checker still sits in `.golangci.yml`'s `disable:` list,
`nolintlint` reports the directive as unused and reddens `make check`.

Several package PRs are in flight at once, so more than one may create this file. **The first
one merged wins; every later PR rebases onto it and appends its section rather than recreating
the file.**

An error-IDENTITY assertion (`ErrorIs`/`ErrorAs`, and `ErrorContains` on a message) aborts
whenever the sentinel or message does not match — INCLUDING when the error is non-nil and merely
different — so it hides every follower in exactly the case a reader most needs to understand.
`NotErrorIs` is the mirror image and belongs to the same class for the same reason: it aborts
when the chain DOES match the sentinel it is asserting absent, which is likewise a failure on a
non-nil error that takes its followers down with it.
Where that follower is an independent non-clause property (a negative `NotContains`, a leak
check, a state or count check, a distinct second phase) the site is reordered so the independent
assertion runs first, or reverted to `assert` and listed here. Where the follower is another
CLAUSE of the same error's message it stays converted: a wrong message fails both clauses
together, so nothing is really hidden.

Every row is a site where the checker is mechanically right and substantively wrong. The
recurring shape is `require-error` on an assertion whose FOLLOWING assertion pins an
independent property: `require` aborts, so converting would hide a real regression behind an
unrelated failure. A site is only listed after that independence was checked by reading the
test, not inferred from the diff.

## app (#1092 / W3-P4)

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `app/app_builder_test.go:491` | require-error | The cause string is one property; lines below then drive `CreateApp().Build()` and pin that startup aborts with a nil app and a live logger. | `//nolint:testifylint // require would abort before the Build-propagation assertions` |
| `app/app_builder_test.go:539` | require-error | Paired with `:540` — two clauses of ONE wrapped error ("cache manager", "maxsize cannot be negative"). | `//nolint:testifylint // second clause of the same wrapped error follows` |
| `app/app_builder_test.go:577` | require-error | The ADR-067 point is the lines below: app nil, logger live, and all three bundle managers observably closed. Aborting on a message change hides the leak check. | `//nolint:testifylint // require would abort before the manager-close assertions` |
| `app/app_test.go:1196` | require-error | Paired with `:1197` — the two errors an aggregate wraps. `TestShutdownAggregatesErrors` exists to pin BOTH. | `//nolint:testifylint // second wrapped error asserted on the next line` |
| `app/app_test.go:1197` | require-error | Same pair, other half. | `//nolint:testifylint // paired aggregate-error assertion` |
| `app/bootstrap_test.go:604` | require-error | Already guarded by a `require.Error` above, so nil-deref is impossible; `:604`/`:605` are two clauses of one error. | `//nolint:testifylint // guarded by require.Error above; second clause follows` |
| `app/bootstrap_test.go:605` | require-error | Same pair, other half. | `//nolint:testifylint // paired error-clause assertion` |
| `app/lifecycle_test.go:175` | require-error | Followed by a `Less(duration, 1s)` bound — correctness and timing are independent properties of one shutdown. | `//nolint:testifylint // timing bound asserted independently below` |
| `app/lifecycle_test.go:358` | require-error | Followed by the log-recorder check that the pre-warm WARN was NOT emitted — a differently-sourced property the test's comment declares. | `//nolint:testifylint // log-emission assertion follows, from a different source` |
| `app/lifecycle_test.go:1081` | require-error | Both assertion messages state a conjunction: teardown never fails the shutdown AND closers still run. | `//nolint:testifylint // closer-ran assertion follows` |
| `app/messaging_setup_test.go:109` | require-error | Paired with `:110` (sentinel + message clause), then an independent call-count assertion. | `//nolint:testifylint // paired clause plus an independent call-count assertion follow` |
| `app/messaging_setup_test.go:110` | require-error | Same pair, other half. | `//nolint:testifylint // paired error-clause assertion` |
| `app/module_test.go:289` | require-error | Paired with `:290` — both errors joined into one shutdown error, then both module names in the message. | `//nolint:testifylint // second joined error asserted on the next line` |
| `app/module_test.go:290` | require-error | Same pair, other half. | `//nolint:testifylint // paired aggregate-error assertion` |
| `app/module_test.go:319` | require-error | Followed by `Contains("failing-module")` and `NotContains("ok-module")`; the EXCLUSION of the clean module is an independent property. | `//nolint:testifylint // module-name inclusion/exclusion assertions follow` |
| `app/prewarm_test.go:199` | require-error | Followed by `Less(elapsed, time.Second)` — error identity and the elapsed bound are independent properties of one cancellation. | `//nolint:testifylint // elapsed-time bound asserted independently below` |
| `app/readiness_test.go:98` | require-error | Probe results carry several independently-regressable fields; lease bookkeeping (`acquired`/`released`) is computed by a different path and asserted below. | `//nolint:testifylint // lease-bookkeeping assertions follow, from a different code path` |
| `app/readiness_test.go:100` | require-error | Same table, no-error arm; same lease assertions follow. | `//nolint:testifylint // lease-bookkeeping assertions follow` |
| `app/readiness_test.go:213` | require-error | Followed by `True(result.Critical)` under a comment stating criticality is retained deliberately — a different code path from `Err`. | `//nolint:testifylint // criticality assertion follows, from a different code path` |
| `app/readiness_test.go:271` | require-error | Followed by `False(got.Critical, "messaging is never critical")`. | `//nolint:testifylint // criticality assertion follows` |
| `app/readiness_test.go:308` | require-error | Followed by the #860 regression pin, a timeout bound — which IS the point of the test. | `//nolint:testifylint // probe-timeout bound is the regression this test pins` |
| `app/readiness_test.go:318` | require-error | Followed by `Contains(Details, "active_caches")` — counters must render on the not-configured path regardless of the error. | `//nolint:testifylint // details-rendering assertion follows` |
| `app/readiness_test.go:360` | require-error | Followed by `False(got.Critical)` and `Contains(Details, "stored_offsets")`. | `//nolint:testifylint // criticality and details assertions follow` |
| `app/slot_test.go:531` | require-error | `fatal` and `advisory` are two distinct return values; a require on `fatal` erases the whole advisory arm the test is named for. | `//nolint:testifylint // the advisory return value is asserted separately below` |

## jose (#1092 / W3-P7)

Three shapes. The `encoded-compare` and `float-compare` rows are the checker being
mechanically right and substantively wrong about what the assertion pins; those two shapes are
not ordering cases, so reordering could not resolve them. The `require-error` rows below them
ARE ordering cases, but of the kind that reordering cannot fix either: they are independent
PEER probes of different branches, not a leader with a follower.

Sites where a sentinel check preceded an independent assertion were REORDERED rather than
listed — the independent assertions now run first and the sentinel is last as `require`. The
distinction that decides it: converting an error-EXISTENCE check (`assert.Error`,
`assert.NoError`) is safe when the followers need the error to exist at all, because an abort
loses nothing they could still have checked; converting a sentinel-IDENTITY check
(`assert.ErrorIs`) is not, because a wrong sentinel leaves every other property of the error
still checkable, and aborting throws that away.

| site | checker | why it stays as written | directive FINAL inserts |
| --- | --- | --- | --- |
| `jose/sealed/splice_test.go:103` | encoded-compare | `TestSpliceReplacesOnlyTheSpan` pins that splice rewrites the located span and nothing else, so key order and spacing are the property. `JSONEq` compares semantically and would pass a splice that reordered the document. | `//nolint:testifylint // byte-exact output is the property; JSONEq ignores key order` |
| `jose/sealed/splice_test.go:104` | encoded-compare | Same test, the "input must not be mutated" half: byte identity of the caller's buffer. | `//nolint:testifylint // asserts the input buffer is byte-identical` |
| `jose/sealed/splice_test.go:113` | encoded-compare | `TestSpliceRawInsertsReplacementVerbatim` — "verbatim" is byte-exactness by name. | `//nolint:testifylint // verbatim insertion is a byte-level property` |
| `jose/sealed/splice_test.go:114` | encoded-compare | Same test, the unmutated-input half. | `//nolint:testifylint // asserts the input buffer is byte-identical` |
| `jose/sealed/seal_test.go:150` | float-compare | `iat` is whole seconds, decoded from JSON as `float64`, inside a block pinning "exactly the decided protected header set and values". A tolerance would let a drifting `iat` pass, which is the one thing the assertion exists to catch. | `//nolint:testifylint // exact issued-at; a tolerance would accept a drifting iat` |
| `jose/internal/cryptoadapter/extra_test.go:94` | require-error | `TestExtraRoundTripInt64` probes four INDEPENDENT branches of `ExtraInt64` in one test — fractional, non-numeric, magnitude, absent. `require` on any one abandons the rest, including the 2^53 boundary case that must PASS. | `//nolint:testifylint // independent branch probe; require would abort the peers` |
| `jose/internal/cryptoadapter/extra_test.go:96` | require-error | Same test, the non-numeric branch. | `//nolint:testifylint // independent branch probe` |
| `jose/internal/cryptoadapter/extra_test.go:98` | require-error | Same test, the magnitude branch. | `//nolint:testifylint // independent branch probe` |
| `jose/internal/cryptoadapter/extra_test.go:104` | require-error | Same test, the absent branch, which also pins that absence is NOT reported as malformed. | `//nolint:testifylint // independent branch probe` |
| `jose/internal/cryptoadapter/extra_test.go:122` | require-error | `TestExtraRoundTripStringSlice`: the inner not-a-string branch, independent of the outer not-an-array branch below it. | `//nolint:testifylint // independent branch probe` |
| `jose/internal/cryptoadapter/extra_test.go:124` | require-error | Same test, the outer not-an-array branch. | `//nolint:testifylint // independent branch probe` |
| `jose/internal/cryptoadapter/extra_test.go:165` | require-error | `Sign` and `Encrypt` call `checkExtra` at separate sites; the following line probes `Encrypt` independently, so a `Sign` regression must not hide an `Encrypt` one. | `//nolint:testifylint // Encrypt's own collision check follows` |
| `jose/internal/cryptoadapter/extra_test.go:280` | require-error | The oversized-segment branch; the line below probes the separate segment-COUNT branch with the same function. | `//nolint:testifylint // separate segment-count branch follows` |
| `jose/internal/cryptoadapter/extra_test.go:302` | require-error | `ExtraInt64`'s nil-guard; the line below probes `ExtraStringSlice`'s own nil-guard, a different method. | `//nolint:testifylint // a different accessor's nil-guard follows` |
| `jose/sealed/seal_test.go:328` | require-error | The family-mismatch branch of `Options.Validate`; the line below exercises the separate nil-receiver branch, so one regression must not mask the other. | `//nolint:testifylint // nil-receiver branch probed below` |

Harvest note for the FINAL author: P5, P6 and P9 documented their false positives in their PR
bodies rather than here (this file postdates them) — pull those rows in before converting.

## config (#1092 / W3-P3)

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `config/config_test.go:1743` | require-error | `err` is REASSIGNED below and re-asserted for the derived-map sub-case; a require here skips that second phase entirely. | `//nolint:testifylint // a second sub-case reassigns and re-asserts err` |
| `config/converters_test.go:155` | float-compare | `toFloat64`'s table is ParseFloat round-trips (`"123.45"` -> `123.45`), where the Go literal and `ParseFloat` produce identical float64 bits. Exact equality IS the converter's contract; a tolerance would let a lossy conversion pass. | `//nolint:testifylint // exact equality is the converter's contract` |
| `config/injection_test.go:110` | require-error | A second phase below sets the env var, reloads the config and pins the default-value behavior; a require here aborts on any message drift and that phase never runs. | `//nolint:testifylint // a reload-and-defaults phase follows` |
| `config/converters_test.go:23` | require-error | `floatToInt64(NaN)` rejection, followed by the `Inf` rejection through a different branch of the converter — a require hides the Inf case whenever NaN regresses. | `//nolint:testifylint // the Inf-rejection case follows through a different branch` |
| `config/injection_test.go:170` | float-compare | Env-var round-trip of `1024.5`, exactly representable in float64. The test pins that the value arrives intact, not that it arrives close. | `//nolint:testifylint // exact equality is the injection contract` |
| `config/tenant_store_test.go:231` | require-error | Followed by removing a non-existent tenant and asserting the store did not mutate — a distinct second phase, independent of the lookup error. | `//nolint:testifylint // an independent no-mutation property follows` |
| `config/tenant_store_test.go:76` | require-error | Followed by an Error assertion on `BrokerURL`, a different resolver path through the same store. | `//nolint:testifylint // a different resolver path is asserted next` |

## Deferred, not excepted

`config/getters_test.go` carries 10 live findings (6 `require-error`, 3 `float-compare`, 1
`empty`) and is deliberately UNTOUCHED by W3-P3: Lane M's #1438 rewrote that file, so it joins a
later sweep or FINAL. They are not exceptions and have no rationale yet — whoever picks the file
up must triage them, and FINAL will red on them until someone does. Recorded here so the count
is not mistaken for a clean package.
