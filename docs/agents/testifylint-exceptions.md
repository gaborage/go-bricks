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
| `app/app_builder_test.go:491` | require-error | The cause string is one property; lines below then drive `CreateApp().Build()` and pin that startup aborts with a nil app and a live logger — a second phase that cannot be hoisted above it. | `//nolint:testifylint // require would abort before the Build-propagation assertions` |
| `app/app_builder_test.go:539` | require-error | Paired with `:540` — two clauses of ONE wrapped error ("cache manager", "maxsize cannot be negative"); the final clause is now `require`. | `//nolint:testifylint // second clause of the same wrapped error follows` |
| `app/app_builder_test.go:579` | require-error | The ADR-067 point is the lines below: the bundle is read off the builder and all three managers are probed observably closed. Aborting on a message change hides the leak check. | `//nolint:testifylint // require would abort before the manager-close assertions` |
| `app/app_test.go:1197` | require-error | Paired with the line below — the two errors an aggregate wraps. `TestShutdownAggregatesErrors` exists to pin BOTH; the final one is now `require`. | `//nolint:testifylint // second wrapped error asserted on the next line` |
| `app/bootstrap_test.go:605` | require-error | Guarded by a `require.Error` above, so nil-deref is impossible; this is the first of two clauses of one error, the second of which is now `require`. | `//nolint:testifylint // guarded by require.Error above; second clause follows` |
| `app/lifecycle_test.go:1081` | require-error | A table subtest whose branch below returns early on the nil arm, so it cannot be hoisted above this assertion; both messages state a conjunction — teardown never fails the shutdown AND closers still run. | `//nolint:testifylint // branch-dependent log assertions follow` |
| `app/messaging_setup_test.go:110` | require-error | Paired with the line below (sentinel + message clause); the message clause is now `require` and the independent call-count assertion has moved above both. | `//nolint:testifylint // paired error-clause assertion follows` |
| `app/module_test.go:294` | require-error | Paired with the line below — both errors joined into one shutdown error; the second is now `require`. | `//nolint:testifylint // second joined error asserted on the next line` |
| `app/factory_resolver_integration_test.go:492` | require-error | The tenant-B isolation check below reads the SAME `sharedKey` through tenant B's client, which is what makes it an isolation check, and is the property this test exists to pin; a require aborts on any non-nil error that is not `ErrNotFound` and the regression goes unreported. | `//nolint:testifylint // the tenant-isolation check below is a separate phase` |

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
