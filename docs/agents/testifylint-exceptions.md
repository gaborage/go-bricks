# testifylint exceptions

Package PRs in the #1092 wave APPEND their deliberate non-conversions here as they land; the
FINAL PR (`ci(lint): enable the remaining testifylint checkers`) converts every row into the
`//nolint` directive its last column names, then drops this file's pending status. Directives
cannot be added earlier — while a checker still sits in `.golangci.yml`'s `disable:` list,
`nolintlint` reports the directive as unused and reddens `make check`.

Several package PRs are in flight at once, so more than one may create this file. **The first
one merged wins; every later PR rebases onto it and appends its section rather than recreating
the file.**

An error-IDENTITY assertion (`ErrorIs`/`ErrorAs`/`NotErrorIs`, and `ErrorContains` on a message)
aborts whenever the sentinel or message does not match — INCLUDING when the error is non-nil and
merely different — so it hides every follower in exactly the case a reader most wants explained.
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

## config (#1092 / W3-P3)

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `config/config_test.go:1743` | require-error | `err` is REASSIGNED below and re-asserted for the derived-map sub-case; a require here skips that second phase entirely. | `//nolint:testifylint // a second sub-case reassigns and re-asserts err` |
| `config/converters_test.go:155` | float-compare | `toFloat64`'s table is ParseFloat round-trips (`"123.45"` -> `123.45`), where the Go literal and `ParseFloat` produce identical float64 bits. Exact equality IS the converter's contract; a tolerance would let a lossy conversion pass. | `//nolint:testifylint // exact equality is the converter's contract` |
| `config/injection_test.go:110` | require-error | A second phase below sets the env var, reloads the config and pins the default-value behaviour; a require here aborts on any message drift and that phase never runs. | `//nolint:testifylint // a reload-and-defaults phase follows` |
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

## app (#1092 / W3-P4) — annotation, NOT a replacement section

W3-P4 (PR #1456) carries its own 24 app rows plus a harvest note, in ITS copy of this file. This
paragraph is an annotation to be merged INTO that section — **whoever resolves the conflict must
keep P4's rows and add this note; resolving in favour of this file alone destroys 24 site
locations.**

The rows were recorded before the fleet rule was settled, and most fail it: a site stays `assert`
only when the following assertion is BOTH independent of the error AND the property the test
exists to pin. It converts whenever that assertion reads the error, for one of two reasons:
a bare `err.Error()` (including inside `Contains`/`NotContains`) would PANIC on a nil error, so
`require` turns a panic into a clean failure; `errors.Is` and `ErrorContains` merely return false
or fail redundantly on nil, so `require` there buys one clean failure rather than two, not safety.

By that rule roughly 18 of P4's 24 rows should be CONVERTED rather than annotated: the seven
`readiness_test.go` rows and the paired-clause rows in `bootstrap_test.go`, `module_test.go`,
`app_builder_test.go` and `app_test.go` are correlated halves of one outcome, not independent
properties. The likely survivors are `prewarm_test.go:199` and `lifecycle_test.go:175` (an
elapsed-time bound is genuinely independent), with `lifecycle_test.go:358` (a log-emission check
from a different source) and `slot_test.go:531` (a second return value) as probable keeps.
PR #1456 was already merge-ready when the rule landed, so this is FINAL's work, not a reopen.
