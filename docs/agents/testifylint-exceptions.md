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
