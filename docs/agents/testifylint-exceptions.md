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

## tail packages (#1092 / W3-P9)

Unlike the `require-error` rows above, every row here is `encoded-compare`, whose failure mode is
different: it keys off the identifier NAME rather than the value, and it cannot see that a test is
pinning BYTES rather than a JSON document. `JSONEq` unmarshals both sides, so it passes on any
reformatting — which is exactly the regression these four assertions exist to catch.

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `internal/sealcli/sealcli_test.go:231` | encoded-compare | Flagged only because the expected value is the variable `payloadJSON`. The property is a byte-exact read-back — the test's own comment says "stdin holds different bytes, so reading it instead would be visible", which `JSONEq` erases. | `//nolint:testifylint // byte-exact read-back, not JSON equivalence` |
| `internal/sealcli/sealcli_test.go:242` | encoded-compare | Same variable, the stdin arm of the table; same byte-exactness. | `//nolint:testifylint // byte-exact read-back, not JSON equivalence` |
| `tools/migration/internal/awssm/fetcher_test.go:35` | encoded-compare | The value IS JSON, but the property is that the fetcher returns `SecretString` VERBATIM. `JSONEq` would keep passing if the fetcher reformatted the secret. | `//nolint:testifylint // verbatim secret bytes, not JSON equivalence` |
| `tools/migration/internal/awssm/fetcher_test.go:50` | encoded-compare | Same contract on the `SecretBinary` path. | `//nolint:testifylint // verbatim secret bytes, not JSON equivalence` |

### float-compare — integers round-tripped through JSON (logger, otel)

`json.Unmarshal` into `map[string]any` decodes every number as `float64`, so a test that logs an
`Int`/`Int64`/`Uint64`/`Dur` and reads it back necessarily compares `float64` values that are
EXACT integers. The contract is exact representation, not proximity, so `InDelta`/`InEpsilon`
would weaken all of them — and would destroy `:564`/`:565` outright, whose whole point is what
float64 does at the precision boundary (`float64(9223372036854775807)` is the deliberately
rounded expectation). Same ruling the plan already applies to `observability/testing/helpers.go:273`:
exact equality is the contract, so the directive is a nolint, never a tolerance.

| site | checker | why it stays `assert.Equal` | directive FINAL inserts |
| --- | --- | --- | --- |
| `logger/adapter_test.go:118,134,150,167` | float-compare | Integer field logged then JSON-decoded; exact round-trip is the property. | `//nolint:testifylint // integer round-tripped through JSON; exact equality is the contract` |
| `logger/adapter_test.go:255,256,345,346,347,348` | float-compare | Same, across the multi-field and structured-entry cases. | `//nolint:testifylint // integer round-tripped through JSON; exact equality is the contract` |
| `logger/adapter_test.go:564,565` | float-compare | `max_int64`/`max_uint64` — the test EXISTS to pin float64 behaviour at the precision boundary; a tolerance erases it. | `//nolint:testifylint // pins float64 precision loss at the int64/uint64 boundary` |
| `logger/adapter_test.go:621,642,662,682` | float-compare | Sensitive-field filter tests: the assertion is that a non-sensitive numeric field is NOT masked, i.e. the exact value survives. | `//nolint:testifylint // integer round-tripped through JSON; exact equality is the contract` |
| `logger/otel_bridge_test.go:314` | float-compare | OTel gauge `AsFloat64()` of an exact integer 7. | `//nolint:testifylint // exact integer value, not a computed float` |

### require-error — the block cannot be reordered into guard-first shape

The canonical shape is `require.Error(t, err)` first, then every follower that is independent of
which sentinel matched (leak, state, counter and message-clause checks — now safe to dereference
`err`), then the identity assertions last in their original order with the FINAL one as `require`.
Applied wherever it fits; the sites below are the residue where it does not.

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `internal/configdecode/configdecode_test.go:63` | require-error | Loop over expected substrings — a peer SET, not a sequence; `require` would abort at the first missing substring and hide the rest. | `//nolint:testifylint // loop over substring peers; abort would hide the rest` |
| `internal/publishdoor/publishdoor_test.go:71` | require-error | The follower performs a SECOND swap; it is a distinct phase, not a property of this error, so it cannot be hoisted above it. | `//nolint:testifylint // a second swap phase follows` |
| `internal/sealcli/sealcli_test.go:103` | require-error | Non-final identity peer: two sentinels (`flag.ErrHelp`, `ErrUsage`) on one error, either regressing alone. The final peer carries the `require`. | `//nolint:testifylint // non-final identity peer; second sentinel is the require` |
| `internal/sealcli/sealcli_test.go:219` | require-error | The follower is an independent `keys.PublicKey` lookup — a second phase that cannot run before the first is asserted. | `//nolint:testifylint // an independent second lookup follows` |
| `keystore/generation_test.go:111` | require-error | Loop over names with the name as the message: a peer set accumulating one result per case. | `//nolint:testifylint // loop over case peers; abort would hide the rest` |
| `keystore/generation_test.go:228` | require-error | Follower is a second `PrivateKey` call with its own error — a distinct phase. | `//nolint:testifylint // an independent second lookup follows` |
| `keystore/keystore_test.go:347` | require-error | Non-final identity peer: this message check sits beside an `ErrorIs(fs.ErrNotExist)` on the same error, which carries the `require`. | `//nolint:testifylint // non-final identity peer; the sentinel check is the require` |
| `keystore/keystore_test.go:420` | require-error | Non-final identity peer: two independent clauses of one message, the second carrying the `require`. | `//nolint:testifylint // non-final identity peer; second clause is the require` |
| `keystore/keystore_test.go:510` | require-error | Follower is a `PrivateKey` lookup asserting a DIFFERENT error variable — the same shape as the shipped `assertions.go:46` line reserved for PB. | `//nolint:testifylint // an independent private-key lookup follows` |
| `keystore/keystore_test.go:611` | require-error | Non-final identity peer: prefix clause beside the "elided" clause, which carries the `require`. | `//nolint:testifylint // non-final identity peer; the elided clause is the require` |
| `keystore/module_test.go:153` | require-error | Non-final identity peer: the byte-floor clause beside the ADR-095 clause, which carries the `require`. | `//nolint:testifylint // non-final identity peer; the ADR clause is the require` |

`internal/tenantstore/tenantstore_test.go:382` is deliberately absent: its only follower is
`Nil(db)`, the correlated half of one `(value, err)` outcome, so the identity check is the final
assertion and carries the `require`.
