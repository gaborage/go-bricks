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
| `config/injection_test.go:110` | require-error | A second phase below sets the env var, reloads the config and pins the default-value behavior; a require here aborts on any message drift and that phase never runs. | `//nolint:testifylint // a reload-and-defaults phase follows` |
| `config/converters_test.go:23` | require-error | `floatToInt64(NaN)` rejection, followed by the `Inf` rejection through a different branch of the converter — a require hides the Inf case whenever NaN regresses. | `//nolint:testifylint // the Inf-rejection case follows through a different branch` |
| `config/tenant_store_test.go:231` | require-error | Followed by removing a non-existent tenant and asserting the store did not mutate — a distinct second phase, independent of the lookup error. | `//nolint:testifylint // an independent no-mutation property follows` |
| `config/tenant_store_test.go:76` | require-error | Followed by an Error assertion on `BrokerURL`, a different resolver path through the same store. | `//nolint:testifylint // a different resolver path is asserted next` |

## deferred sets, triaged (#1092 / W3-FINAL-a)

The five files earlier recorded as "Deferred, not excepted" carried 53 live findings between
them. FINAL-a triaged all 53 against a fresh whole-repo measurement: every one is now either
converted or carries a row here with its reason, so none is left unexamined. Three of the rows —
`config/getters_test.go:141`, `:144`, `:147` — are excepted only PENDING #1471, which dissolves
them by splitting `TestNilConfigAccessors` into per-probe subtests; they are a deferred fix, not a
permanent exception, and #1471 is the tracked home for that.

The nine rows below are this five-file cohort's complete residue, and they correspond one-to-one
with the cohort's live findings. That bijection is scoped to the cohort — the document as a whole
covers 125 sites across every section, and FINAL-b is what checks the whole-file correspondence
before converting each row to a directive.

Do not read the nine as "nine of the 53". Four of them (`app/managers_test.go:802`, `:806`, `:828`,
`:829`) are SECOND-ORDER: findings the conversions themselves created. Hoisting an independent
check above a pair of message clauses puts an error assertion in front of other assertions, which
is what `require-error` reports; demoting a clause back to `assert` does the same. The same effect
raised a `formatter` finding in `server/middleware_test.go` when a `contains` site was converted.
That is the operational lesson for FINAL-b: converting under one checker can raise a finding under
another, so re-measure after every conversion pass rather than once at the end.

Three techniques removed exceptions that a first pass had written down, and are worth reusing:
an error assertion placed LAST in its block draws no `require-error` finding at all, so extracting
the call under test to a local (`closeErr := p.Close()`) and asserting on it after the state checks
beats a directive; an independent follower that only reads the error can be HOISTED above the
message clauses instead of holding them at `assert`; and where the mechanically-right form is
avoided for a reason, check whether the reason still holds — the `strings.Contains` sites in
`server/middleware_test.go` were kept off `assert.NotContains` to avoid printing a panic payload,
but the payload is a synthetic constant declared in that same file and the neighbouring
`server/panic_guard_test.go` already spells the same absence with `NotContains`.

No `config` site is excepted for `float-compare`. Where exact equality genuinely IS the
contract — a default passed straight back, a `ParseFloat` round-trip, an env-var value that must
arrive intact — `assert.InDelta(t, want, got, 0)` states that with no permanent directive to
maintain, so it is the fix rather than a `//nolint`; where the expected value is zero,
`assert.Zero` is simpler still and draws no finding. The two rows this file used to carry for
`config/converters_test.go:155` and `config/injection_test.go:170` were converted that way and
dropped. FINAL-b applies the same rule to the `float-compare` rows in the other sections: each one
whose contract is exact equality converts to `InDelta(…, 0)` rather than gaining a directive, and
a row survives only where `InDelta` cannot express the check (`logger/adapter_test.go:564,565`
pins float64 precision loss at the int64 boundary, so it stays).

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `app/managers_test.go:802` | require-error | Deliberately hoisted ABOVE the two message clauses so a clause mismatch cannot hide it: `%w`-not-`%v` is an independent property, and it is the regression this subtest exists to catch. A require here would invert that and hide the clauses instead. | `//nolint:testifylint // hoisted above the message clauses on purpose; require would hide them` |
| `app/managers_test.go:806` | require-error | `"cache manager"` is the WRAPPER's prefix (`app/managers.go:276`) while `tt.wantCause` on the next line is the wrapped cache error's own detail (`cache/manager.go`). Two code paths, so a wrapper-prefix regression must not abort before the cause clause is checked; the final clause is the `require`. | `//nolint:testifylint // wrapper prefix; the independent inner-cause clause follows` |
| `app/managers_test.go:828` | require-error | Wrapper clause (`maxsize=%d` in `app/managers.go:276`), followed by a second wrapper clause and then by the wrapped `cache/manager.go` cause. Aborting here hides both. | `//nolint:testifylint // wrapper clause; a second clause and the inner cause follow` |
| `app/managers_test.go:829` | require-error | The key half of the same format string. It still precedes the independent inner-cause clause, so it stays non-fatal even though its sibling above tests the SAME rendering — the rule is about what follows, not about which clauses share a format string. | `//nolint:testifylint // wrapper clause; the independent inner-cause clause follows` |
| `config/getters_test.go:141` | require-error | `TestNilConfigAccessors` probes a zero-valued `&Config{}` for nil-safety; the `RequiredString` and `Unmarshal` probes below reach different accessors and the function has no other aborting assertion to piggyback on. Splitting the probes into subtests removes this row and the two below it — #1471. | `//nolint:testifylint // the RequiredString and Unmarshal nil-safety probes below are independent` |
| `config/getters_test.go:144` | require-error | Same function; followed by the `Unmarshal` probe and by the `Exists`/`All`/`Custom` checks, which pin the zero value's SHAPE rather than its errors. Dissolved by #1471. | `//nolint:testifylint // the Unmarshal probe and the Exists/All/Custom checks below are independent` |
| `config/getters_test.go:147` | require-error | Same function; followed by the `Exists`/`All`/`Custom` checks, which are what pin that a zero-valued config stays empty rather than materializing defaults. Dissolved by #1471. | `//nolint:testifylint // the Exists/All/Custom checks below are independent` |
| `internal/resourcepool/resourcepool_test.go:295` | require-error | A require aborts before `unblock()` two lines down, so the leader goroutine stays blocked on `<-release` for the life of the test binary — the file's own `getOrCreateBounded` calls `unblock()` before its `t.Fatal` for exactly that reason. It would also skip the second half of the documented contract: that the abandoning waiter did NOT cancel the leader's in-flight create. | `//nolint:testifylint // a require would skip unblock() and leak the leader goroutine; a second phase follows` |
| `internal/resourcepool/resourcepool_test.go:1187` | require-error | First of two `errors.Join` members. `TestPoolCloseClosesAllAndJoinsErrors` exists to pin that `errors.Is` matches EACH one, so a require on the key-2 member would hide whether key-3 still surfaces; the key-3 assertion on the next line is the `require`. | `//nolint:testifylint // the second joined close error is asserted on the next line` |

## cache, httpclient (#1092 / W3-P5)

`encoded-compare` keys off the identifier NAME, not the value: `testJSONType` is flagged only
because its name contains "JSON". Its value is `"application/json"`, a Content-Type header, and
`JSONEq` would try to parse a media type as a document. Verified: the same literal inline is not
flagged, and renaming the constant clears it.

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `httpclient/client_test.go:487` | encoded-compare | Compares a request's `Content-Type` header against `testJSONType`; the value is a media type, not a JSON document. | `//nolint:testifylint // Content-Type header value, not a JSON document` |
| `httpclient/client_test.go:606` | encoded-compare | Same assertion in the default-content-type test. | `//nolint:testifylint // Content-Type header value, not a JSON document` |

## database (#1092 / W3-P1)

The rule applied here, in the order it decides.

1. **The follower would PANIC.** `err.Error()` — bare, or inside a `Contains` — dereferences a
   nil error, so the existence check above it converts to `require` and a crash becomes a clean
   failure.
2. **The follower would fail REDUNDANTLY.** `errors.Is/As` and `ErrorContains(t, err, …)` are
   nil-safe: they report a second time on the same fault. Converting buys one clean failure
   instead of two, so it is a signal decision, not a safety one.
3. **The follower is an INDEPENDENT property** — an instrumentation counter, a mock's recorded
   calls, a rollback flag, a manager size, a log-buffer leak check. Here the assertions were
   REORDERED so the independent one runs first and the error assertion still converts, rather
   than recording an exception: 15 sites were resolved that way.
4. **Two SPECIFICITY checks land on one error** — a wrap-chain check and a message check. They
   test different defect classes (`%v`-for-`%w` breaks one, a text edit the other) and no
   ordering makes both final, so the non-final one stays `assert` and takes a row. Direction-free.

Rules 1 and 2 govern error-EXISTENCE checks. An error-IDENTITY assertion is different: it can
fail while `err` is merely the WRONG error, aborting every follower, so those are reordered
under rule 3 — UNLESS the follower consumes what the identity assertion produced (an `ErrorAs`
target, which would be nil) or dereferences `err` and so needs the non-nil guarantee above it.
An existence guard is never hoisted past; only specificity assertions move.

A row below therefore means the order CANNOT change: the follower is itself an error assertion
over a different input, or a later phase that depends on this one having run.

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

## messaging (#1092 / W3-P2)

Two shapes: a nil/zero-receiver safety sweep whose followers probe other methods, and PEER
sentinel probes where several claims are made about ONE error. In both, an aborting `require`
would report the first claim and hide the rest; the final assertion in each block is `require`,
so `require-error` is satisfied.

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `messaging/payload_error_test.go:81` | require-error | Peer sentinel probe over a table's `match`/`notMatch` pair: the positive and negative mappings are separate claims about one stage, and aborting on the first hides which one regressed. | `//nolint:testifylint // peer sentinel probe; the paired negative claim follows` |
| `messaging/payload_error_test.go:82` | require-error | Same block, second peer; a third sentinel (`ErrNotConnected`) is claimed after it. | `//nolint:testifylint // peer sentinel probe; a third sentinel check follows` |
| `messaging/payload_error_test.go:94` | require-error | Peer probe inside a loop over unknown stages: an unrecognized stage must match NEITHER sentinel, so reporting only the first leaves the other unverified for that stage. | `//nolint:testifylint // peer sentinel probe; the second sentinel claim follows` |
| `messaging/payload_error_test.go:412` | require-error | `TestPayloadErrorNilAndZeroValueAreSafe` pins that EVERY method on a nil `*PayloadError` is safe; the followers read `Fields()` and `Is()` on the receiver, never the asserted `Unwrap()` result, so `require` would abort the sweep at the first method. | `//nolint:testifylint // require would abort the remaining nil-receiver method checks` |
| `messaging/payload_error_test.go:418` | require-error | Same test, zero-value half: the followers exercise the other methods on `zero`, not the asserted `Unwrap()` result. | `//nolint:testifylint // require would abort the remaining zero-value method checks` |
| `messaging/payload_error_test.go:421` | require-error | Peer sentinel probe: `ErrPayloadUndecodable` and `ErrPayloadInvalid` are two claims about one zero value, and reporting only the first hides which mapping regressed. | `//nolint:testifylint // peer sentinel probe; the next assertion probes a different sentinel` |
| `messaging/payload_error_test.go:432` | require-error | Peer sentinel probe over one error: the positive mapping and the two negative ones are separate classifications. | `//nolint:testifylint // peer sentinel probe; two further sentinels follow` |
| `messaging/payload_error_test.go:433` | require-error | Same block, second peer. | `//nolint:testifylint // peer sentinel probe; a further sentinel follows` |
| `messaging/payload_error_test.go:434` | require-error | Peer of the two sentinels above it, and the `ErrorAs`/`Same`/`NotContains` checks that follow read a different target: a wrong classification must not hide the open-refused cause or the redaction check. | `//nolint:testifylint // peer sentinel probe; the cause and redaction checks follow` |
| `messaging/publish_destination_test.go:47` | require-error | Peer of the `NotErrorIs(ErrPublishRetriesExhausted)` below it: the destination error must be the invalid-destination one AND not a retry-exhaustion, and the block's `Zero(publishAttempts)` proves the channel was never touched. | `//nolint:testifylint // peer sentinel probe; the retry-exhaustion claim follows` |
| `messaging/sealed/opener_test.go:336` | require-error | Peer of the `NotErrorIs` that follows: the refusal must carry one sentinel and not the other. | `//nolint:testifylint // peer sentinel probe; the negative claim follows` |
| `messaging/sealed_consumer_test.go:281` | require-error | Peer sentinel probe: the `NotErrorIs` below it and a second `ErrorAs` target follow, so an unexpected match must not hide them. | `//nolint:testifylint // peer sentinel probe; the negative claim follows` |
| `messaging/sealed_consumer_test.go:282` | require-error | Peer of the sentinel above it; the second `ErrorAs` target and its `Same` check follow. | `//nolint:testifylint // peer sentinel probe; a second ErrorAs target follows` |
| `messaging/streams/payload_error_test.go:31` | require-error | Peer sentinel probe over the table's match/notMatch pair, same shape as the AMQP lane's. | `//nolint:testifylint // peer sentinel probe; the paired negative claim follows` |
| `messaging/streams/payload_error_test.go:32` | require-error | Same block, second peer; a third sentinel follows. | `//nolint:testifylint // peer sentinel probe; a third sentinel check follows` |
| `messaging/streams/payload_error_test.go:44` | require-error | Peer probe inside the unknown-stage loop. | `//nolint:testifylint // peer sentinel probe; the second sentinel claim follows` |
| `messaging/streams/payload_error_test.go:77` | require-error | Peer of the sentinel-mapping assertion the comment below it introduces: the unwrap chain and the sentinel mapping are separate claims. | `//nolint:testifylint // peer sentinel probe; the sentinel mapping follows` |
| `messaging/streams/runner_test.go:410` | require-error | Peer probe over a DIFFERENT partition's error: each partition's failure is classified independently, and aborting on one hides the other. | `//nolint:testifylint // peer probe over a different partition's error` |
| `messaging/typed_consumer_test.go:120` | require-error | Peer sentinel probe: the business error must surface AND not be reclassified as a payload error; the negatives that follow are the other half. | `//nolint:testifylint // peer sentinel probe; the negative claims follow` |
| `messaging/typed_consumer_test.go:121` | require-error | Same block, second peer. | `//nolint:testifylint // peer sentinel probe; a further negative claim follows` |
| `messaging/typed_consumer_test.go:122` | require-error | Same block, third peer; a `NotErrorAs` type check follows. | `//nolint:testifylint // peer sentinel probe; a NotErrorAs type check follows` |

## observability

| site | checker | why | directive |
| --- | --- | --- | --- |
| `observability/dual_processor_test.go:821` | require-error | Two sentinel checks on ONE aggregated error: `Shutdown` joins the action and trace processors' failures and the test proves both are present. A `require` on either hides the other, and no ordering clears the checker because whichever error assertion is not last is the one it flags. | `//nolint:testifylint // both sentinels of one aggregated error; a require hides the other` |
| `observability/dual_processor_test.go:855` | require-error | Same shape for the aggregated `ForceFlush` error: both `errAction` and `errTrace` must be shown present, so neither check may abort the other. | `//nolint:testifylint // both sentinels of one aggregated error; a require hides the other` |
| `observability/processor_attribute_exporter_test.go:219` | require-error | The memoization property IS the pair: the first `Shutdown` and the memoized second must report the same sentinel. A `require` on the first hides whether the second was memoized, which is the only thing this test exists to prove. | `//nolint:testifylint // first and memoized second result; a require hides the second` |
| `observability/testing/helpers.go:286` | float-compare | Shipped helper; exact-equality contract. `AssertSpanAttribute` proves a span carries the attribute value the caller passed — a round trip through the OTel attribute set, not a computation — so a tolerance would let a genuinely different value pass. The neighbouring metric helpers use `InDelta` because those values ARE computed; this one must not. | `//nolint:testifylint // exact equality is this helper's contract` |

## outbox

Error-EXISTENCE checks converted to `require` where the follower dereferenced
the error or would only have failed redundantly; error-IDENTITY checks did not,
and were reordered so the identity assertion comes last. One `error-is-as` and
two `empty` findings were fixed by the assertion the checker named. One site
could not be resolved by ordering:

| site | checker | why | directive |
| --- | --- | --- | --- |
| `outbox/publisher_test.go:716` | require-error | `ErrorContains` and the `ErrorIs` that follows it are independent properties of one error: a `%v`-for-`%w` regression breaks only the sentinel chain, a message edit breaks only the text. A `require` on the message check would hide the sentinel check, so the message assertion stays non-fatal and the sentinel assertion stays last. | `//nolint:testifylint // message and sentinel are independent; a require hides the sentinel` |

## scheduler, migration (#1092 / W3-P6)

Peer identity probes: each block asserts several sentinels against ONE error, and the
non-final peers stay `assert` so a mismatch on one still reports the others. The final
peer in each block is `require`, so `require-error` is satisfied.

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `migration/flyway_test.go:1335` | require-error | Peer of `NotErrorIs(err, ErrFlywayOutputUnparsed)` below it: "too short" and "not a parse failure" are separate classifications of one error, and the block's `NotContains(err.Error(), "pw12345")` password check must run whichever way the sentinel lands. | `//nolint:testifylint // peer sentinel probe; the next assertion classifies the same error` |
| `migration/flyway_test.go:1524` | require-error | Peer of `NotErrorIs(err, ErrFlywayTimeout)`: a cancel-kill must classify as canceled AND not as a timeout, and reporting only the first hides which half regressed. | `//nolint:testifylint // peer sentinel probe; the next assertion classifies the same error` |
| `migration/proc_unix_test.go:51` | require-error | Peer probe over a DIFFERENT subject: this asserts the OS view (`syscall.Kill` reports ESRCH), the next asserts the cmd view (`Cancel()` reports ErrProcessDone). Two independent observations of one kill. | `//nolint:testifylint // peer probe over a different subject; the cmd-side claim follows` |
| `migration/result_test.go:144` | require-error | Peer sentinel probe: the outcome must be the parse failure AND not the reported-failure classification. | `//nolint:testifylint // peer sentinel probe; the negative claim follows` |
| `migration/result_test.go:151` | require-error | Peer sentinel probe: the subprocess error takes precedence AND the parse error must not surface. | `//nolint:testifylint // peer sentinel probe; the precedence claim's negative half follows` |
| `migration/result_test.go:157` | require-error | Peer sentinel probe: the wrapper sentinel and the underlying cause are separate claims about one error chain. | `//nolint:testifylint // peer sentinel probe; the underlying-cause claim follows` |
| `migration/secrets_test.go:299` | require-error | Peer of the `ErrorContains` clause that follows: the sentinel and the message are separate claims about one malformed-secret error. | `//nolint:testifylint // peer sentinel probe; the message clause follows` |

## server (#1092 / W3-P7)

`server/middleware_test.go` is **deferred, not excepted**: Lane L (#1453) rewrote the file, so
its 12 findings belong to a later sweep rather than to this table. They are real and must be
fixed there — do not read their absence here as a disposition. Reconcile this section with
`EXCLUDE_RE='server/middleware_test\.go'`, which yields `live: 14 rows: 14` — the count is
this table's rows, and it must be updated whenever a row is added or removed.

| site | checker | why it stays `assert` | directive FINAL inserts |
| --- | --- | --- | --- |
| `server/detail_leak_test.go:220` | require-error | `TestBindErrorNilSafety` fires five PEER probes at different receivers (a nil `*bindError`, a zero-valued one, one carrying a cause) to prove none panics. They are not a leader/follower pair, so reordering cannot help: `require` on any one of them would abort the sweep and leave the remaining receivers unprobed. | `//nolint:testifylint // peer nil-safety probes; require would abort the sweep` |
| `server/detail_leak_test.go:222` | require-error | Same sweep, the zero-valued receiver. | `//nolint:testifylint // peer nil-safety probe` |
| `server/handler_test.go:1833` | float-compare | Envelope `meta.total` is an integer count decoded from JSON as `float64`. Exact equality is the contract; a tolerance would accept a wrong count. | `//nolint:testifylint // integer count decoded as float64; exactness is the point` |
| `server/handler_test.go:1834` | float-compare | Same, `meta.limit`. | `//nolint:testifylint // integer pagination value` |
| `server/handler_test.go:1835` | float-compare | Same, `meta.offset`. | `//nolint:testifylint // integer pagination value` |
| `server/handler_test.go:1923` | float-compare | Same, a handler-supplied `meta.page`. | `//nolint:testifylint // integer pagination value` |
| `server/jose_test.go:269` | float-compare | Same shape on the decrypted JOSE envelope's `meta.total`. | `//nolint:testifylint // integer count decoded as float64` |
| `server/jose_test.go:324` | float-compare | Same, `meta.page` on the JOSE path. | `//nolint:testifylint // integer pagination value` |
| `server/timing_test.go:68` | require-error | Inside `if tt.expectHeader`; the unconditional `assert.Equal(http.StatusOK, rec.Code)` at `:86` sits AFTER the whole if/else. A header-format regression must not abort before the status-code check, which is independent of it. | `//nolint:testifylint // unconditional status check follows the enclosing block` |
| `server/timing_test.go:112` | require-error | Same shape in `TestTimingErrorHandler`: the `assert.Equal(http.StatusBadRequest, rec.Code)` below the block is independent of whether the duration parses. | `//nolint:testifylint // status check follows the enclosing block` |
| `server/performance_stats_test.go:101` | require-error | The error-expectation if/else is followed by the whole counter block — `amqpCount`, `dbCount`, `amqpElapsed`, `dbElapsed`. The handler sets those BEFORE returning, so they are checkable whatever the error was; aborting here throws away the counters this test exists to verify. | `//nolint:testifylint // counter assertions follow the enclosing block` |
| `server/performance_stats_test.go:103` | require-error | Same if/else, the no-error arm. | `//nolint:testifylint // counter assertions follow the enclosing block` |
| `server/validator_test.go:613` | require-error | The nil-pointer case; the lines below run a SECOND, independent `Validate` call proving a valid pointer passes. One input's regression must not hide the other's. | `//nolint:testifylint // an independent second Validate call follows` |
| `server/validator_test.go:648` | require-error | Same shape for the empty-slice case and its valid-slice counterpart. | `//nolint:testifylint // an independent second Validate call follows` |

Converted rather than listed, for the record: a site whose follower would PANIC on a nil error
(`err.Error()`, bare or inside a `Contains`) converts, and so does one whose follower would
merely fail redundantly (`errors.Is`, `ErrorContains`, both of which return false on a nil
error rather than panicking) — there the conversion buys one clean failure instead of two.

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
