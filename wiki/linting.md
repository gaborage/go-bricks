# Linting (Deep Dive)

This document is for teams building services **with** GoBricks who want the framework's
lint posture in their own repository. It explains what GoBricks enforces, gives a
consumer-ready `.golangci.yml`, and — more usefully — records which linters were measured
and **rejected**, so you do not re-litigate decisions that were already tested against a
real GoBricks codebase.

The enforcement target is the [uber-go style guide](https://github.com/uber-go/guide/blob/master/style.md).
There is no single tool for it; coverage is assembled from ~45 `golangci-lint` linters, an
explicit `revive` rule set, and the `formatters:` block.

## There is no config inheritance

`golangci-lint` v2 has **no `extends` and no `include`**. Adding one fails schema
validation:

```console
$ golangci-lint config verify -c probe.yml
jsonschema: "" does not validate with "/additionalProperties":
additional properties 'extends' not allowed
```

Within a single repository, a sub-module with no config of its own walks **up** to the
nearest `.golangci.yml` (this is how `tools/migration` inherits GoBricks' root config).
Across repositories there is no mechanism at all — you copy the file. Plan for that: a copy
is a fork, and it will drift.

## Adopting the config

Start from GoBricks' [`.golangci.yml`](../.golangci.yml) and make three edits.

### 1. Substitute the `gci` prefix

```yaml
formatters:
  settings:
    gci:
      sections:
        - standard
        - default
        - prefix(github.com/your-org/your-service)   # <- your module path
```

**Do not copy the section order blindly.** GoBricks uses `standard, default, prefix(...)`
because that already matched its dominant convention: switching to the plain
`standard, default` default would rewrite 197 files, merging third-party imports back into
the first-party block. Measure your own tree before choosing:

```bash
# how many files would each order rewrite?
golangci-lint fmt --diff ./... | grep -c '^--- '
```

Pick the order that moves you *toward* your existing convention, not away from it.

### 2. Delete the framework-only exclusions

Every entry under `linters.exclusions.rules` is a GoBricks path and means nothing in your
repo — `logger/adapter.go` (zerologlint), `cmd/seal-payload/` (an importas carve-out), and
a few rules scoped to `_test.go`. Delete them all and add your own as findings justify.
Drop the `forbidigo` settings block too: its patterns enforce a GoBricks architecture
decision (ADR-083) and mean nothing outside this repo.

The `testifylint` block is portable in full — copy it as it stands:

```yaml
linters:
  enable:
    - testifylint
  settings:
    testifylint:
      enable-all: true
      go-require:
        ignore-http-handlers: false
```

**No checker is disabled.** The block once carried a `disable:` list, and it is worth knowing
why so you do not mistake it for the end state if you find it in an older tag. Turning
`enable-all` on over an existing suite surfaced ~1,200 findings across fourteen checkers at
once — too many to fix in one reviewable change and too many to leave red. The list was a
ratchet: every checker on, the ones with outstanding findings switched off, then one package
at a time cleared its findings and deleted its entry (#1092). It is gone now, and the residual
sites carry inline directives instead.

`go-require`'s `ignore-http-handlers: false` is the load-bearing half and is written out
rather than left to default: it keeps the checker looking inside `http.HandlerFunc` bodies,
where a `require` call aborts the server goroutine instead of the test.

**Directive convention.** A site that should stay `assert` carries the reason inline:

```go
assert.ErrorIs(t, err, errTwo, "Close surfaces the key-2 close error") //nolint:testifylint // the second joined close error is asserted on the next line
```

One directive per site, on the assertion's own line, with a `//` reason that names the
property at stake — not the checker. One trap is worth knowing before you write one:
`encoded-compare` keys off the IDENTIFIER NAME rather than the value, so a constant holding
`"application/json"` is flagged wherever its name contains `JSON`, while the same literal inline
is not. Renaming only clears it if the new name drops that token — which for a media-type
constant means making the name worse, so a directive is the right answer there. Check the
identifier before assuming the checker read your value. Keep `nolintlint` on with all three of
`allow-unused: false`, `require-explanation: true` and `require-specific: true`. They cover
different failures and you want every one: `allow-unused` turns a directive that stopped applying
into a lint error instead of quiet debt, `require-specific` stops a bare `//nolint` from silencing
every linter at once, and `require-explanation` is what keeps the reason there at all — which
matters most once the reason on the line is the only record, with no exceptions document behind
it. Together they make the convention self-enforcing rather than aspirational.

Adopt it the same way: enable everything, measure, then convert rather than suppress. Most
findings are mechanical, and two shapes account for nearly all of the judgment calls.

**The `require-error` doctrine.** The checker wants every `assert.Error*` to be `require`.
Decide per site:

- *Existence converts.* `assert.Error` / `assert.NoError` — the guard everything below depends
  on — becomes `require`. So does any assertion whose target is consumed below it: an
  `assert.ErrorAs` whose target is dereferenced on the next line is a nil-panic waiting for its
  first regression, not a style choice. **Producers stay `require`.**
- *Specificity aborts.* An identity or message assertion (`ErrorIs`, `ErrorAs`, `ErrorContains`,
  `NotErrorIs`) fails on any non-matching error, including a non-nil one, so converting it hides
  every follower in exactly the case a reader most needs. Convert only when nothing independently
  checkable follows — and look one scope up, not just to the end of the block: a `t.Run` body's
  last line still has the parent's assertions after it.
- *Consecutive message clauses are not automatically siblings.* Several `ErrorContains` calls
  against one `err` may be converted down to the last one only when they test the SAME rendering —
  one format string. When one clause comes from a wrapper and another from the error it wrapped
  with `%w`, they are produced by different code and are independent properties: a wrapper-prefix
  regression must not abort before the inner cause is checked. Note the rule is about what FOLLOWS
  a clause, not about which clauses were rendered together — two clauses sharing a format string
  still both stay non-fatal if an independent clause comes after them.
- *Prefer position over a directive.* An error assertion placed LAST in its block draws no
  finding at all. If the assertion wraps the call under test, extract the call
  (`closeErr := p.Close()`), keep the state checks in place, and assert on the local afterwards.
- *Releasers stay reachable.* Never convert an assertion sitting above a statement that releases
  something — `unblock()`, `cancel()`, `close(ch)`, a `rel()`. `require` aborts via `Goexit`,
  which runs deferred calls but not the rest of the block, so the release never happens: the
  counterpart goroutine blocks for the life of the test binary and any second phase is lost.
  The question is not what the assertion reads, it is what the abort SKIPS.

For `float-compare`, if exact equality really is the contract say so with
`assert.InDelta(t, want, got, 0)` (or `assert.Zero`); a zero delta IS exact equality and costs
no permanent directive. Note it compares numerically, so unlike `assert.Equal` it will not also
pin that a JSON-decoded value arrived as `float64` — add `require.IsType` where that matters.

Two measurement traps. First, measure your disable list against **itself**, not against
`enable-all`: testifylint's checkers are priority-ordered and one checker claims a given call
site, so a sweep with everything enabled hides a lower-priority checker behind a higher one —
disable the higher one and the finding reappears under a different name. The same effect bites
during conversion, where fixing a site under one checker can expose it to another, so re-measure
after every pass rather than once at the end. Second, if you measure with the vet driver
(`go vet -vettool=<testifylint> -enable-all ./...`), give each run a throwaway `GOCACHE` and
delete it afterwards: vet caches its results, so a second run over an unchanged tree prints
nothing at all, which is indistinguishable from a clean tree. And `./...` under `GOWORK=off`
covers only the module you are standing in — a repo with a second module needs its own run.

Recheck your own exclusions periodically: an exclusion that matches on message `text` stops
matching when the linter rewords the message, and it fails **silently** in either
direction. GoBricks carried two `text: "var-naming: avoid package names"` stanzas that were
dead from revive v1.15.0 onward, when that check moved to a separate rule and the wording
changed. Those two stanzas were suppressing nothing, and nothing said so — deleting them
changed no findings. Prefer scoping by `path` and `linters` over matching message text.

Keep the `presets` list (`comments`, `common-false-positives`, `legacy`,
`std-error-handling`) — those are generic.

### 3. Trim the `importas` map

Two entries are worth keeping, because consumers import these packages too:

```yaml
linters:
  settings:
    importas:
      alias:
        - pkg: github.com/gaborage/go-bricks/database/testing
          alias: dbtesting
        - pkg: github.com/gaborage/go-bricks/jose/testing
          alias: jositest
```

Leave `no-unaliased` and `no-extra-aliases` **off**. Either one turns a handful of alias
fixes into a repo-wide sweep with no style-guide mandate behind it.

## Framework settings you probably want to loosen

GoBricks holds itself to a stricter standard than it expects of applications built on it
(80% coverage, production-grade stability; see the Developer Manifesto in `CLAUDE.md`).
These three are calibrated for a framework:

| Setting | GoBricks | Consider for a service |
| --- | --- | --- |
| `dupl.threshold` | 100 | 150+, or drop `dupl` — handler/test symmetry trips it |
| `gocyclo.min-complexity` | 15 | 15–20; raise before adding `//nolint` |
| `lll.line-length` | 215 | whatever your editor is set to |

## Linters measured and rejected

These were each measured against the GoBricks tree and deliberately **not** adopted. The
counts are GoBricks' — re-measure against yours — but the *reasons* mostly transfer,
because they follow from patterns the framework encourages.

| Linter | Findings | Why not |
| --- | --- | --- |
| `ireturn` | 663 | Fights the framework's design. `database.Interface`, `messaging.AMQPClient`, and `cache.Cache` are returned as interfaces on purpose — that is the vendor-agnosticism principle. Your service will inherit those signatures. |
| `err113` | 662 | Flags every `fmt.Errorf` without a sentinel. Defensible in a library with a stable error surface; punishing in service code. |
| `wrapcheck` | 321 | Directly contradicts "wrap once at boundaries". Adopting it means wrapping at every call depth. |
| `revive: unused-receiver` | 302 | Pure style, no guide backing. |
| `revive: struct-tag` | 24 | False-positives the jose `_ struct{}` marker idiom and any runtime-registered custom validator (`validate:"mcc_code"`). |
| `revive: datarace` | 3 | All false positives — a syntactic check that cannot see mutex protection. `go test -race` is the better tool, but it detects races only on paths a test actually executes and does not prove their absence. |
| `predeclared` | 0 | Redundant with revive's `redefines-builtin-id`, which is already in the default set. |
| `gochecknoglobals` | 106 | Sentinel errors and `sync.Pool` instances are legitimate globals. |
| `mnd` | 156 | Magic-number detection is noisy against HTTP status codes and durations. |

`go-ruleguard` (via `gocritic`) was also evaluated. It **works**, and it is the only way to
express guide rules no linter covers ("Channel Size is One or None"). It was rejected for
GoBricks because it requires a real `go.mod` dependency on `github.com/quasilyte/go-ruleguard/dsl`,
which for a public library lands in every consumer's module graph. **In a service, that
objection does not apply** — if you want executable house rules, ruleguard is a reasonable
choice for you even though it was wrong for us. Two things to know: the rules-file path is
resolved relative to the working directory (sub-modules break), and you must set
`failOn: dsl,import` (or `all`) or a rules file that fails to load is silently empty.
`failOn: dsl` alone is not enough — it catches DSL syntax errors but still logs-and-skips
when an import cannot be resolved, which is the most likely failure since the rules file
must import `.../go-ruleguard/dsl`.

## Measuring before you adopt

Do not trust the counts above, or any single run. Three traps, all of which produced wrong
numbers during GoBricks' own adoption:

**`issues.uniq-by-line` defaults to `true`.** Only the first issue per line is reported, so
enabling many linters at once *undercounts* each one. `perfsprint` measured 44 in a
combined run and 149 alone — `err113` and `wrapcheck` were claiming the same
`fmt.Errorf` lines. Measure one linter at a time:

```bash
LINTER=perfsprint   # one candidate at a time
golangci-lint run --enable-only="$LINTER" --uniq-by-line=false ./...
```

**An unknown `revive` rule name exits 0.** It logs `level=error ... cannot find rule: <name>`
and the run still succeeds, so a typo silently disables a rule while CI stays green. Grep
for it:

```bash
golangci-lint run ./... 2>&1 | grep -E 'level=error|cannot find rule'
```

**`revive.rules` REPLACES the default set** when `enable-default-rules` is omitted. It does
not extend it, so declaring any rule silently drops every default you did not re-list —
with no warning, because a missing rule and a passing rule both report nothing. Set
`enable-default-rules: true` and list only your additions:

```yaml
linters:
  settings:
    revive:
      enable-default-rules: true   # keep the golint-equivalent set
      rules:
        - name: early-return       # additions only
```

This is what GoBricks does. It previously re-declared all 23 defaults above its additions,
which worked but meant deleting one line lost a rule silently. The two spellings are
equivalent at v2.12.2 — golangci-lint's default list is a verbatim copy of revive's, and
both resolve to the same rule set — so the flag is safer against accidental omissions. It
cannot be combined with `enable-all-rules`.

Because of all three, a reading of "0 findings" is ambiguous between *no violations*, *the
rule never ran*, and *another linter claimed the line*. Prove a rule fires by planting a
deliberate violation in a throwaway package before believing a zero.

## The formatters block

In v2, formatters are **not** linters. Putting `gofumpt` under `linters.enable` is a hard
error (`can't load config: gofumpt is a formatter`); they belong in a top-level
`formatters:` block. Two consequences:

- `golangci-lint run` reports formatter output as ordinary issues
  (`File is not properly formatted (gci)`), so the config and the reformat must land in the
  same commit or CI goes red immediately.
- `go fmt` cannot fix `gofumpt`/`gci` findings. If your `fmt` target runs `go fmt` and your
  `check` target is `fmt lint ...`, it will reformat and then fail lint anyway. Point `fmt`
  at `golangci-lint fmt`.

`golangci-lint fmt` walks **files**, while `run` loads **packages** under the build tags it
was invoked with. So `fmt` reaches build-tagged files (`//go:build integration`)
unconditionally, whereas `run` sees them only when passed a matching
`--build-tags=integration`. GoBricks' `make lint` and CI jobs do not pass it, so `fmt` is
the only thing keeping those files formatted here — check your own invocations before
assuming the same.

## Related

- [testing.md](testing.md) — test conventions the linters assume (camelCase test names)
- [architecture_decisions.md](architecture_decisions.md) — ADR index
