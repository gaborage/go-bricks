# testifylint exceptions

Package PRs in the #1092 wave APPEND their deliberate non-conversions here as they land; the
FINAL PR (`ci(lint): enable the remaining testifylint checkers`) converts every row into the
`//nolint` directive its last column names, then drops this file's pending status. Directives
cannot be added earlier — while a checker still sits in `.golangci.yml`'s `disable:` list,
`nolintlint` reports the directive as unused and reddens `make check`.

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

Harvest note for the FINAL author: P5, P6 and P9 documented their false positives in their PR
bodies rather than here (this file postdates them) — pull those rows in before converting.
