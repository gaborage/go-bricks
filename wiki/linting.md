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
repo — `logger/adapter.go` (zerologlint), `(cache|database|observability)/testing/` and
`trace/` (revive var-naming), `^cmd/seal-payload/` (an importas carve-out). Delete them all
and add your own as findings justify.

Keep the `presets` list (`comments`, `common-false-positives`, `legacy`,
`std-error-handling`) — those are generic.

### 3. Trim the `importas` map

Two entries are worth keeping, because consumers import these packages too:

```yaml
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
| `revive: datarace` | 3 | All false positives — a syntactic check that cannot see mutex protection. `go test -race` covers the class soundly. |
| `predeclared` | 0 | Redundant with revive's `redefines-builtin-id`, which is already in the default set. |
| `gochecknoglobals` | 106 | Sentinel errors and `sync.Pool` instances are legitimate globals. |
| `mnd` | 156 | Magic-number detection is noisy against HTTP status codes and durations. |

`go-ruleguard` (via `gocritic`) was also evaluated. It **works**, and it is the only way to
express guide rules no linter covers ("Channel Size is One or None"). It was rejected for
GoBricks because it requires a real `go.mod` dependency on `github.com/quasilyte/go-ruleguard/dsl`,
which for a public library lands in every consumer's module graph. **In a service, that
objection does not apply** — if you want executable house rules, ruleguard is a reasonable
choice for you even though it was wrong for us. Two things to know: the rules-file path is
resolved relative to the working directory (sub-modules break), and `failOn: dsl` is
mandatory or a rules file that fails to load is silently empty.

## Measuring before you adopt

Do not trust the counts above, or any single run. Three traps, all of which produced wrong
numbers during GoBricks' own adoption:

**`issues.uniq-by-line` defaults to `true`.** Only the first issue per line is reported, so
enabling many linters at once *undercounts* each one. `perfsprint` measured 44 in a
combined run and 149 alone — `err113` and `wrapcheck` were claiming the same
`fmt.Errorf` lines. Measure one linter at a time:

```bash
golangci-lint run --enable-only=<linter> --uniq-by-line=false ./...
```

**An unknown `revive` rule name exits 0.** It logs `level=error ... cannot find rule: <name>`
and the run still succeeds, so a typo silently disables a rule while CI stays green. Grep
for it:

```bash
golangci-lint run ./... 2>&1 | grep -E 'level=error|cannot find rule'
```

**`revive.rules` REPLACES the default set.** It does not extend it. If you declare any
rules, you must re-declare every default you still want — GoBricks' config lists all 23
above its additions for exactly this reason. Dropping one removes enforcement with no
signal.

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

`golangci-lint fmt` walks **files**, while `run` loads **packages** — so `fmt` reaches
build-tagged files (`//go:build integration`) that `run` never sees. If you have
build-tagged code, `fmt` is the only thing keeping it formatted.

## Related

- [testing.md](testing.md) — test conventions the linters assume (camelCase test names)
- [architecture_decisions.md](architecture_decisions.md) — ADR index
