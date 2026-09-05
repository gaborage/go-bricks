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

## database (#1092 / W3-P1)

The rule applied here, in the order it decides. A site CONVERTS when the assertion that follows
DEREFERENCES the error, for one of two reasons. `err.Error()` — bare, or inside a `Contains` —
PANICS on a nil error, so `require` turns a crash into a clean failure. `errors.Is/As` and
`ErrorContains(t, err, …)` do NOT panic on nil; they simply fail a second time, so `require`
there buys one clean failure instead of two reports of the same fault. The rule covers
error-EXISTENCE checks only: an error-IDENTITY assertion (`ErrorIs`/`ErrorAs`/`NotErrorIs`)
that fails while `err` is merely the WRONG error still aborts every follower, so those are
reordered — independent assertions first, identity last — unless the follower consumes what
the identity assertion produced (an `ErrorAs` target) or dereferences `err` and so needs the
non-nil guarantee to sit above it. Where TWO specificity checks land on one error — a wrap-chain
check and a message check — no ordering can make both final, so the non-final one stays `assert`
and takes a row; the rule is direction-free.

Where the follower is instead an INDEPENDENT property — an instrumentation counter, a mock's
recorded calls, a rollback flag, a manager size, a log-buffer leak check — the assertions were
REORDERED so the independent one runs first and the error assertion still converts, rather than
recording an exception: 15 sites were resolved that way. A row below therefore means the order
CANNOT change — the follower is itself an error assertion over a different input, or a later phase
that depends on this one having run.

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `database/identifier/identifier_test.go:61` | require-error | Block of independent per-input validations (`:58`-`:63`), each its own `Validate(vendor, input)` over a DIFFERENT input; requiring abandons the rest of the batch. Order cannot help — every line in the run is an error assertion. | `//nolint:testifylint // batch of independent per-input validations` |
| `database/identifier/identifier_test.go:62` | require-error | Same block as `:61`. | `//nolint:testifylint // batch of independent per-input validations` |
| `database/internal/builder/renderer_test.go:46` | require-error | Pairs with `:47` over a DIFFERENT input (`a#b` vs `plain_name`); both lines are error assertions, so reordering cannot save the second. | `//nolint:testifylint // paired one-line checks over different inputs` |
| `database/manager_test.go:748` | require-error | Followed by `require.ErrorContains(t, err, tenantA)` — a MESSAGE check, a different defect class from this wrap-chain check: a `%v`-for-`%w` regression breaks only the `ErrorIs`, a text edit breaks only the `ErrorContains`. Both are error assertions on the same `err`, so no ordering makes both final; the non-final one stays `assert`. | `//nolint:testifylint // a message check on the same error follows` |
| `database/oracle/connection_test.go:781` | require-error | `TestConnectionTransactionOperationsErrorHandling` runs three scenarios in one body, each `mock.Expect…` then assert; the Begin scenario cannot move below the BeginTx/Prepare ones without breaking the mock ordering it sets up. | `//nolint:testifylint // independent scenario follows in same test` |
| `database/oracle/connection_test.go:789` | require-error | Same test, BeginTx scenario; the Prepare scenario's mock setup follows it. | `//nolint:testifylint // independent scenario follows in same test` |
| `database/postgresql/connection_test.go:669` | require-error | Mirror of `oracle/connection_test.go:781`. | `//nolint:testifylint // independent scenario follows in same test` |
| `database/postgresql/connection_test.go:677` | require-error | Mirror of `oracle/connection_test.go:789`. | `//nolint:testifylint // independent scenario follows in same test` |
| `database/tracking_test.go:230` | require-error | Followed by the test's SECOND phase, which re-arms the mock (`expectedBeginTxErr`, a fresh `ExpectBegin`) and asserts the BeginTx path; the phase depends on this one having run, so it cannot be hoisted above it. | `//nolint:testifylint // the BeginTx phase is asserted after this` |
