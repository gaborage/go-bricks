# Mutation Gate + Property-Based Testing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a diff-scoped mutation testing gate (`make mutate`) with a nightly full-repo baseline, plus four property-based exemplar test suites, per `docs/superpowers/specs/2026-07-26-constraint-gauntlet-design.md`.

**Architecture:** A small Go wrapper (`scripts/mutatediff`, root module, package main) computes changed line ranges vs merge-base with origin/main, shells out to gremlins per changed package, and applies the verdict policy (LIVED = fail, NOT_COVERED = warn, TIMED_OUT = killed). The nightly workflow reuses a Makefile target. Property suites use `pgregory.net/rapid` and live one file per package.

**Tech Stack:** Go 1.26, gremlins (`github.com/go-gremlins/gremlins`), `pgregory.net/rapid`, GitHub Actions.

## Global Constraints

- Module path: `github.com/gaborage/go-bricks`. Go 1.26.
- Run `make check` before EVERY commit (repo rule). It must pass; never commit red.
- Test function names camelCase (`TestQueryBuilderPlaceholderArityProperty`); table-case names snake_case.
- Comments: bare minimum — only non-obvious intent and mandatory `// #nosec` / `// SECURITY:` annotations.
- Commit messages: Conventional Commits, end body with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`. Commit via `git commit -F <msgfile>` (a repo hook blocks heredoc `-m`); verify with `git log -1` after every commit.
- Commits are SSH-signed via 1Password. If signing fails with `1Password: failed to fill whole buffer`, STOP and ask the user to unlock 1Password. NEVER use `--no-gpg-sign`.
- `.gitignore` is allowlist-style: `!*.go` (line 15), `!.github/workflows/*.yml`, `!docs/superpowers/plans/*.md` already exist. `.gremlins.yaml` is NOT allowlisted yet (Task 1 adds it). Verify every new file stages with `git add -n <path>` before relying on it.
- Two PRs, both off `main`, never stacked: PR1 = Tasks 1–7 (branch `feature/mutation-gate-and-pbt`, already exists with the design doc). PR2 = Tasks 8–13 (branch created off fresh `main` AFTER PR1 merges).
- Pre-push gates before each PR push, in order: `/simplify` → `/security-audit` → `/code-review`. These are main-session skills — subagents must NOT attempt them; hand back to the main session at the gate steps.
- PR bodies: exactly three headings `## What` / `## Impact` / `## Verification`, ≤150 words total, Verification carries only what CI cannot show. Personal repo — no `Refs:` line.
- GitHub account: `gh auth switch -u gaborage` if `gh auth status` shows the work account.

---

### Task 1: Gremlins compatibility spike + pinned configuration

**Files:**
- Modify: `Makefile` (version pin block at top, near `GOVULNCHECK_VERSION`)
- Modify: `.gitignore` (allowlist `.gremlins.yaml`)
- Create: `.gremlins.yaml`

**Interfaces:**
- Produces: Makefile var `GREMLINS_VERSION`, a validated gremlins invocation shape (`unleash --output <file> <pkg>`), and a captured real report JSON used verbatim as the Task 3 fixture.

- [ ] **Step 1: Verify gremlins runs on Go 1.26**

```bash
go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0 --version
go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0 unleash --help
```

Record from `--help`: exact flag names for output file, workers, timeout coefficient, build tags, and dry-run. If the binary fails to build or run on Go 1.26, check for a newer tag (`git ls-remote --tags https://github.com/go-gremlins/gremlins | tail -5`) and retry with it.

**STOP GATE:** If no gremlins version runs on Go 1.26, STOP. Report to the user: the spec's fallback is the avito-tech/go-mutesting fork, which changes Tasks 1/3/4 invocation details — that is a plan revision, not an executor improvisation.

- [ ] **Step 2: Verify per-package scoping and capture a real report**

```bash
go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0 unleash --dry-run --output /tmp/gremlins-dry.json ./config
go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0 unleash --output /tmp/gremlins-config.json ./config
python3 -c "import json;d=json.load(open('/tmp/gremlins-config.json'));print(list(d.keys()));print({f['file_name'] for f in d.get('files',[])})"
```

Confirm: (a) every `file_name` in the report is under `config/` — per-package scoping works; (b) top-level keys include a files array with per-mutation `line`/`status`/`type` (names may differ — record the real ones); (c) statuses seen include KILLED/LIVED/NOT_COVERED variants and note their exact spelling.

**STOP GATE:** If gremlins mutates files outside the given package path, diff-scoping is impossible with this engine — STOP and report (same fallback consequence as Step 1).

- [ ] **Step 3: Write `.gremlins.yaml`, allowlist it, pin the version**

`.gremlins.yaml` (adjust key names to what `--help` documented in Step 1; these are the v0.5.x names):

```yaml
unleash:
  workers: 4
  timeout-coefficient: 3
```

`.gitignore` — add directly below the `!.golangci.yml`-style tool-config allowlist entries (keep neighbors alphabetical if they are):

```
!.gremlins.yaml
```

`Makefile` — add to the version pin block (after `GOLANGCI_LINT_VERSION`), using the version validated in Step 1:

```make
GREMLINS_VERSION := v0.5.0
```

- [ ] **Step 4: Verify staging and commit**

```bash
git add -n .gremlins.yaml   # must list the file, not silence
make check
git add .gremlins.yaml .gitignore Makefile
git commit -F <msgfile>     # "build: pin gremlins and add mutation config"
git log -1
```

Save `/tmp/gremlins-config.json` — Task 3 pastes a trimmed excerpt of it as the parser fixture.

---

### Task 2: Wrapper diff parsing (`diff.go`)

**Files:**
- Create: `scripts/mutatediff/diff.go`
- Create: `scripts/mutatediff/diff_test.go`

**Interfaces:**
- Produces (consumed by Tasks 3–4):
  - `type lineRange struct { Start, End int }` — half-open `[Start, End)` new-file line numbers
  - `func parseUnifiedDiff(diff string) map[string][]lineRange`
  - `func mutationScope(changes map[string][]lineRange) map[string][]lineRange`
  - `func packagesOf(files map[string][]lineRange) []string` — sorted, deduped, `./pkg/dir` form

- [ ] **Step 1: Write the failing tests**

`scripts/mutatediff/diff_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

const sampleDiff = `diff --git a/config/injection.go b/config/injection.go
index 1111111..2222222 100644
--- a/config/injection.go
+++ b/config/injection.go
@@ -25,0 +26,3 @@ func (c *Config) InjectInto(target any) error {
+	a := 1
+	b := 2
+	_ = a + b
@@ -40 +44 @@ func x() {
-	old := 1
+	new := 1
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1,5 +0,0 @@
-gone
diff --git a/config/new_file.go b/config/new_file.go
new file mode 100644
--- /dev/null
+++ b/config/new_file.go
@@ -0,0 +1,2 @@
+package config
+var z = 3
`

func TestParseUnifiedDiffExtractsNewFileRanges(t *testing.T) {
	got := parseUnifiedDiff(sampleDiff)
	want := map[string][]lineRange{
		"config/injection.go": {{Start: 26, End: 29}, {Start: 44, End: 45}},
		"config/new_file.go":  {{Start: 1, End: 3}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseUnifiedDiff = %#v, want %#v", got, want)
	}
}

func TestParseUnifiedDiffSkipsPureDeletionHunks(t *testing.T) {
	diff := "--- a/f.go\n+++ b/f.go\n@@ -10,2 +9,0 @@\n-x\n-y\n"
	if got := parseUnifiedDiff(diff); len(got["f.go"]) != 0 {
		t.Errorf("pure deletion produced ranges: %#v", got)
	}
}

func TestMutationScopeFiltersNonTargets(t *testing.T) {
	in := map[string][]lineRange{
		"config/injection.go":           {{Start: 1, End: 2}},
		"config/injection_test.go":      {{Start: 1, End: 2}},
		"tools/migration/main.go":       {{Start: 1, End: 2}},
		"database/testdata/fixture.go":  {{Start: 1, End: 2}},
		"wiki/testing.md":               {{Start: 1, End: 2}},
		"scripts/mutatediff/diff.go":    {{Start: 1, End: 2}},
	}
	got := mutationScope(in)
	want := map[string][]lineRange{
		"config/injection.go":        {{Start: 1, End: 2}},
		"scripts/mutatediff/diff.go": {{Start: 1, End: 2}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mutationScope = %#v, want %#v", got, want)
	}
}

func TestPackagesOfDedupesAndSorts(t *testing.T) {
	got := packagesOf(map[string][]lineRange{
		"database/query_builder.go": nil,
		"database/factory.go":       nil,
		"config/injection.go":       nil,
	})
	want := []string{"./config", "./database"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("packagesOf = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 2: Run tests, verify they fail to compile**

Run: `go test ./scripts/mutatediff/`
Expected: FAIL — `undefined: parseUnifiedDiff` etc.

- [ ] **Step 3: Implement `diff.go`**

```go
// Package main implements mutatediff, the diff-scoped mutation gate.
package main

import (
	"bufio"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// lineRange is a half-open [Start, End) range of new-file line numbers.
type lineRange struct {
	Start int
	End   int
}

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func parseUnifiedDiff(diff string) map[string][]lineRange {
	changes := map[string][]lineRange{}
	var current string
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			current = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "+++ /dev/null"):
			current = ""
		case strings.HasPrefix(line, "@@"):
			if current == "" {
				continue
			}
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count == 0 {
				continue
			}
			changes[current] = append(changes[current], lineRange{Start: start, End: start + count})
		}
	}
	return changes
}

func mutationScope(changes map[string][]lineRange) map[string][]lineRange {
	scoped := map[string][]lineRange{}
	for file, ranges := range changes {
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		if strings.HasPrefix(file, "tools/") || strings.Contains(file, "testdata/") {
			continue
		}
		scoped[file] = ranges
	}
	return scoped
}

func packagesOf(files map[string][]lineRange) []string {
	seen := map[string]bool{}
	for file := range files {
		dir := filepath.ToSlash(filepath.Dir(file))
		if dir == "." {
			seen["."] = true
			continue
		}
		seen["./"+dir] = true
	}
	pkgs := make([]string, 0, len(seen))
	for p := range seen {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./scripts/mutatediff/`
Expected: PASS

- [ ] **Step 5: make check, commit**

```bash
make check
git add scripts/mutatediff/diff.go scripts/mutatediff/diff_test.go
git commit -F <msgfile>   # "build(mutate): parse changed line ranges from git diff"
git log -1
```

---

### Task 3: Wrapper report judging (`report.go`)

**Files:**
- Create: `scripts/mutatediff/report.go`
- Create: `scripts/mutatediff/report_test.go`

**Interfaces:**
- Consumes: `lineRange` from Task 2.
- Produces (consumed by Task 4):
  - `type mutantVerdict struct { File string; Line int; Operator, Status string }`
  - `func judge(reportJSON []byte, changed map[string][]lineRange) (failures, warnings []mutantVerdict, err error)`

- [ ] **Step 1: Write the failing tests**

The fixture below reflects the REAL schema captured in the Task 1 spike (`.superpowers/sdd/2026-07-26-mutation-gate-and-pbt/gremlins-config-sample.json`): statuses are spelled with SPACES (`"NOT COVERED"`, `"TIMED OUT"`), and `file_name` is basename-only — gremlins does not prefix the package dir. Because of that, `judge` takes the package dir and joins it with the basename to match the changed-file map. Cross-check the fixture against the captured sample before writing it; if anything still differs, adjust BOTH fixture and struct together.

`scripts/mutatediff/report_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

const fixtureReport = `{
  "go_module": "github.com/gaborage/go-bricks",
  "test_efficacy": 75,
  "mutations_coverage": 90,
  "files": [
    {
      "file_name": "injection.go",
      "mutations": [
        {"line": 27, "type": "CONDITIONALS_NEGATION", "status": "LIVED"},
        {"line": 27, "type": "ARITHMETIC_BASE", "status": "KILLED"},
        {"line": 44, "type": "CONDITIONALS_BOUNDARY", "status": "NOT COVERED"},
        {"line": 200, "type": "INCREMENT_DECREMENT", "status": "LIVED"},
        {"line": 28, "type": "INVERT_NEGATIVES", "status": "TIMED OUT"}
      ]
    },
    {
      "file_name": "other.go",
      "mutations": [
        {"line": 5, "type": "CONDITIONALS_NEGATION", "status": "LIVED"}
      ]
    }
  ]
}`

func TestJudgeAppliesVerdictPolicyOnChangedLines(t *testing.T) {
	changed := map[string][]lineRange{
		"config/injection.go": {{Start: 26, End: 29}, {Start: 44, End: 45}},
	}
	failures, warnings, err := judge([]byte(fixtureReport), "./config", changed)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	wantFail := []mutantVerdict{
		{File: "config/injection.go", Line: 27, Operator: "CONDITIONALS_NEGATION", Status: "LIVED"},
	}
	wantWarn := []mutantVerdict{
		{File: "config/injection.go", Line: 44, Operator: "CONDITIONALS_BOUNDARY", Status: "NOT COVERED"},
	}
	if !reflect.DeepEqual(failures, wantFail) {
		t.Errorf("failures = %#v, want %#v", failures, wantFail)
	}
	if !reflect.DeepEqual(warnings, wantWarn) {
		t.Errorf("warnings = %#v, want %#v", warnings, wantWarn)
	}
}

func TestJudgeRejectsMalformedJSON(t *testing.T) {
	if _, _, err := judge([]byte("{nope"), "./config", nil); err == nil {
		t.Error("expected error for malformed JSON")
	}
}
```

Covered by the single fixture: LIVED in range → failure; LIVED out of range (line 200) → ignored; LIVED in unchanged file (`config/other.go`) → ignored; NOT_COVERED in range → warning; KILLED and TIMED_OUT in range → neither list.

- [ ] **Step 2: Run tests, verify compile failure**

Run: `go test ./scripts/mutatediff/`
Expected: FAIL — `undefined: judge`

- [ ] **Step 3: Implement `report.go`**

```go
package main

import (
	"encoding/json"
	"path"
	"strings"
)

// gremlinsReport mirrors the JSON written by `gremlins unleash --output`.
// Spike-verified truths (Task 1): file_name is basename-only, statuses are
// spelled with spaces ("NOT COVERED", "TIMED OUT"). Keep in sync with the
// fixture in report_test.go.
type gremlinsReport struct {
	Files []struct {
		FileName  string `json:"file_name"`
		Mutations []struct {
			Line   int    `json:"line"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"mutations"`
	} `json:"files"`
}

type mutantVerdict struct {
	File     string
	Line     int
	Operator string
	Status   string
}

// judge buckets report mutants that land on changed lines: LIVED fails the
// gate, NOT COVERED warns (SonarCloud owns coverage), anything else passes.
// pkgDir is the package the report was generated for — gremlins emits
// basenames, so the repo-relative path is pkgDir + file_name.
func judge(reportJSON []byte, pkgDir string, changed map[string][]lineRange) (failures, warnings []mutantVerdict, err error) {
	var rep gremlinsReport
	if err := json.Unmarshal(reportJSON, &rep); err != nil {
		return nil, nil, err
	}
	for _, f := range rep.Files {
		name := path.Join(strings.TrimPrefix(pkgDir, "./"), f.FileName)
		ranges, ok := changed[name]
		if !ok {
			continue
		}
		for _, m := range f.Mutations {
			if !inRanges(m.Line, ranges) {
				continue
			}
			v := mutantVerdict{File: name, Line: m.Line, Operator: m.Type, Status: m.Status}
			switch m.Status {
			case "LIVED":
				failures = append(failures, v)
			case "NOT COVERED":
				warnings = append(warnings, v)
			}
		}
	}
	return failures, warnings, nil
}

func inRanges(line int, ranges []lineRange) bool {
	for _, r := range ranges {
		if line >= r.Start && line < r.End {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./scripts/mutatediff/`
Expected: PASS

- [ ] **Step 5: make check, commit**

```bash
make check
git add scripts/mutatediff/report.go scripts/mutatediff/report_test.go
git commit -F <msgfile>   # "build(mutate): judge gremlins reports against changed lines"
git log -1
```

---

### Task 4: Wrapper orchestration + `make mutate` + live verification

**Files:**
- Create: `scripts/mutatediff/main.go`
- Modify: `Makefile` (new `mutate` and `mutate-baseline` targets, after the `sec:` target)

**Interfaces:**
- Consumes: `parseUnifiedDiff`, `mutationScope`, `packagesOf`, `judge`, `lineRange`, `mutantVerdict` (Tasks 2–3).
- Produces: `make mutate` (diff gate, exit 0 clean / 1 survivors / 2 tool error) and `make mutate-baseline` (full-repo run writing `gremlins-report.json`, used by Task 5).

- [ ] **Step 1: Implement `main.go`**

```go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	engine := flag.String("engine", "", "mutation engine command prefix, e.g. 'go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0'")
	base := flag.String("base", "origin/main", "ref to compute merge-base against")
	flag.Parse()
	if *engine == "" {
		fmt.Fprintln(os.Stderr, "mutatediff: -engine is required")
		os.Exit(2)
	}
	os.Exit(run(*engine, *base, os.Stdout))
}

func run(engine, baseRef string, out io.Writer) int {
	mergeBase, err := gitOutput("merge-base", "HEAD", baseRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutatediff: %v\n", err)
		return 2
	}
	diff, err := gitOutput("diff", "-U0", "--no-color", mergeBase, "HEAD", "--", "*.go")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutatediff: %v\n", err)
		return 2
	}
	changed := mutationScope(parseUnifiedDiff(diff))
	if len(changed) == 0 {
		fmt.Fprintln(out, "mutatediff: no mutatable changes vs merge-base")
		return 0
	}
	var failures, warnings []mutantVerdict
	for _, pkg := range packagesOf(changed) {
		fmt.Fprintf(out, "mutatediff: mutating %s\n", pkg)
		reportName := "gremlins-" + strings.ReplaceAll(strings.TrimPrefix(pkg, "./"), "/", "-") + ".json"
		reportPath := filepath.Join(os.TempDir(), reportName)
		args := append(strings.Fields(engine), "unleash", "--output", reportPath, pkg)
		cmd := exec.Command(args[0], args[1:]...) // #nosec G204 -- dev tool; engine comes from the Makefile pin, not user input
		cmd.Stdout = out
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()
		reportJSON, readErr := os.ReadFile(reportPath) // #nosec G304 -- path built from os.TempDir + package dir
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "mutatediff: no report for %s (read: %v, run: %v)\n", pkg, readErr, runErr)
			return 2
		}
		f, w, jerr := judge(reportJSON, pkg, changed)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "mutatediff: parse report for %s: %v\n", pkg, jerr)
			return 2
		}
		failures = append(failures, f...)
		warnings = append(warnings, w...)
	}
	for _, w := range warnings {
		fmt.Fprintf(out, "WARN not covered: %s:%d %s\n", w.File, w.Line, w.Operator)
	}
	if len(failures) > 0 {
		fmt.Fprintln(out, "FAIL surviving mutants on changed lines:")
		for _, f := range failures {
			fmt.Fprintf(out, "  %s:%d %s\n", f.File, f.Line, f.Operator)
		}
		return 1
	}
	fmt.Fprintln(out, "mutatediff: all mutants on changed lines killed")
	return 0
}

func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 2: Add Makefile targets**

After the `sec:` target:

```make
mutate: ## Diff-scoped mutation gate: mutants on changed lines vs origin/main must die (see wiki/testing.md#mutation-gate)
	go run ./scripts/mutatediff -engine "go run github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION)"

mutate-baseline: ## Full-repo mutation baseline (advisory; consumed by the nightly workflow)
	go run github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION) unleash --output gremlins-report.json
```

- [ ] **Step 3: Live verification — no-op diff exits clean fast**

Run: `go run ./scripts/mutatediff -engine "false" -base HEAD`
Expected: `mutatediff: no mutatable changes vs merge-base`, exit 0, engine never invoked (spec success criterion 2).

- [ ] **Step 4: Live verification — real run on this branch (dogfood)**

Run: `make mutate`
The branch's own changed code is `scripts/mutatediff/*.go`, so the gate mutates the wrapper using the wrapper's tests. If survivors appear in `scripts/mutatediff`, strengthen `diff_test.go`/`report_test.go` until `make mutate` passes — do not weaken the policy.

- [ ] **Step 5: Live verification — weakened test produces FAIL (spec success criterion 1)**

Temporarily comment out the body of `TestJudgeAppliesVerdictPolicyOnChangedLines` (leave `t.Skip("weakened")`), run `make mutate`, and confirm it FAILS listing survivors in `scripts/mutatediff/report.go`. Restore the test, re-run `make mutate`, confirm PASS. Nothing from this step is committed except the restored file.

- [ ] **Step 6: make check, commit**

```bash
make check
git add scripts/mutatediff/main.go Makefile
git commit -F <msgfile>   # "build(mutate): add diff-scoped mutation gate (make mutate)"
git log -1
```

---

### Task 5: Nightly baseline workflow

**Files:**
- Create: `.github/workflows/mutation-nightly.yml`

**Interfaces:**
- Consumes: `make mutate-baseline` (Task 4), which writes `gremlins-report.json` at the repo root.

- [ ] **Step 1: Write the workflow**

Field names below match the Task 1 spike capture (`test_efficacy`, `mutations_coverage`, spaced statuses `"NOT COVERED"`).

```yaml
name: Mutation Baseline

on:
  schedule:
    - cron: '43 3 * * *'   # daily 03:43 UTC — advisory full-repo mutation score
  workflow_dispatch: {}

permissions:
  contents: read

jobs:
  mutation-baseline:
    runs-on: ubuntu-latest
    timeout-minutes: 240
    steps:
      - uses: actions/checkout@v7
        with:
          persist-credentials: false

      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod

      - name: Run full-repo mutation baseline
        run: make mutate-baseline || echo "::warning::gremlins exited non-zero (advisory job)"

      - name: Publish score summary
        if: always()
        run: |
          if [ ! -f gremlins-report.json ]; then
            echo "no report produced" >> "$GITHUB_STEP_SUMMARY"
            exit 0
          fi
          {
            echo "## Mutation baseline"
            jq -r '"efficacy: \(.test_efficacy)% · mutant coverage: \(.mutations_coverage)%"' gremlins-report.json
            echo ""
            echo "| file | lived | not_covered |"
            echo "|---|---|---|"
            jq -r '.files[]
              | [.file_name,
                 ([.mutations[] | select(.status=="LIVED")] | length),
                 ([.mutations[] | select(.status=="NOT COVERED")] | length)]
              | select(.[1] > 0 or .[2] > 0)
              | "| \(.[0]) | \(.[1]) | \(.[2]) |"' gremlins-report.json | head -50
          } >> "$GITHUB_STEP_SUMMARY"

      - name: Upload report artifact
        if: always()
        uses: actions/upload-artifact@v7
        with:
          name: gremlins-report
          path: gremlins-report.json
          retention-days: 30
```

- [ ] **Step 2: Validate and commit**

```bash
git add -n .github/workflows/mutation-nightly.yml   # must stage (allowlisted by !.github/workflows/*.yml)
make check
git add .github/workflows/mutation-nightly.yml
git commit -F <msgfile>   # "ci: add nightly full-repo mutation baseline (advisory)"
git log -1
```

The cron cannot be exercised pre-merge; `workflow_dispatch` exists so it can be triggered manually right after merge (spec success criterion 3).

---

### Task 6: PR1 documentation

**Files:**
- Modify: `CLAUDE.md` (Quick Reference block + Workflow Rules bullet)
- Modify: `wiki/testing.md` (new `## Mutation Gate` section at the end)

- [ ] **Step 1: CLAUDE.md — Quick Reference**

In the "Most Common Commands" fenced block, after the `make test-integration` line, add:

```
make mutate             # Diff-scoped mutation gate: mutants on changed lines vs origin/main must die
```

- [ ] **Step 2: CLAUDE.md — Workflow Rules**

In the Workflow Rules bullet describing the pre-push gates, append one sentence after "The order is load-bearing: …CodeRabbit renders the final independent verdict on the end state.":

```
After the agent gates settle, run `make mutate` once as the final machine gate before pushing (surviving mutants on changed lines block the push; see wiki/testing.md#mutation-gate).
```

- [ ] **Step 3: wiki/testing.md — Mutation Gate section**

Append:

```markdown
## Mutation Gate

`make mutate` runs mutation testing on the diff only: it computes changed line
ranges vs `git merge-base HEAD origin/main`, runs gremlins per changed package,
and applies this policy to mutants that land on changed lines:

| Status | Verdict | Rationale |
|---|---|---|
| `LIVED` | **fail** (exit 1) | A mutant on a line you wrote survived your tests |
| `NOT COVERED` | warn | Coverage is SonarCloud's gate; no double-gating |
| `TIMED OUT` | pass | The mutant hung the code and the test timeout noticed |
| `KILLED` | pass | |

Excluded from scope: `_test.go` files, `testdata/`, and `tools/` (separate Go
module). Engine version is pinned via `GREMLINS_VERSION` in the Makefile;
runtime knobs live in `.gremlins.yaml`. The nightly `Mutation Baseline`
workflow runs the full repo in advisory mode and publishes a per-file score
table to the job summary plus a JSON artifact.

When the gate fails, strengthen the test so the listed mutant dies (assert the
boundary, the sign, the branch the operator flipped) — never respond by
excluding the file.
```

- [ ] **Step 4: make check, commit**

```bash
make check
git add CLAUDE.md wiki/testing.md
git commit -F <msgfile>   # "docs: document the diff-scoped mutation gate"
git log -1
```

---

### Task 7: PR1 gates and push

**MAIN SESSION ONLY** — subagents stop before this task.

- [ ] **Step 1:** `make check` green on the branch tip.
- [ ] **Step 2:** Run `/simplify`; if it changes code, `make check` again, then re-run `make mutate` (the diff changed).
- [ ] **Step 3:** Run `/security-audit`; same re-check rule.
- [ ] **Step 4:** Run `/code-review` (CodeRabbit). If findings are applied, `make check` + re-run `/code-review` so CodeRabbit sees the final diff.
- [ ] **Step 5:** `git push -u origin feature/mutation-gate-and-pbt`, then open the PR:

```markdown
## What
Test-strength verification existed only as manual discipline. `make mutate` now
runs gremlins on changed packages and fails when a mutant on a changed line
survives (LIVED); NOT_COVERED only warns since SonarCloud owns coverage. A
nightly advisory workflow publishes a full-repo per-file mutation score.

## Impact
Run `make mutate` after the agent gates, before every push; surviving mutants
block. Engine pinned via `GREMLINS_VERSION`; knobs in `.gremlins.yaml`.

## Verification
Dogfooded on this branch (gate mutates its own wrapper). Deliberately weakened
test produced FAIL with the survivor named; no-op diff exits clean without
invoking the engine. Nightly cron not exercisable pre-merge — trigger via
workflow_dispatch after merge.
```

- [ ] **Step 6:** `/sonar-pr <N>` once SonarCloud reports; fix or document every NEW issue. Address every CodeRabbit finding in-thread.

---

### Task 8: rapid dependency + query builder property suite

**PRECONDITION:** PR1 merged. Start from fresh `main`:

```bash
git checkout main && git pull
git checkout -b feature/property-based-suites
```

**Files:**
- Modify: `go.mod` / `go.sum` (root)
- Create: `database/database_properties_test.go`

**Interfaces:**
- Consumes: `database.NewQueryBuilder(vendor string)`, consts `database.PostgreSQL` / `database.Oracle`, `qb.Filter() types.FilterFactory` (`Eq(column string, value any) types.Filter`, `And(filters ...types.Filter) types.Filter`), `qb.Select(...).From(...).Where(...).ToSQL() (string, []any, error)`.

- [ ] **Step 1: Add the dependency**

```bash
go get pgregory.net/rapid@latest
go mod tidy
(cd tools/migration && go mod tidy)
git diff --stat go.mod go.sum tools/migration/go.mod tools/migration/go.sum
```

- [ ] **Step 2: Write the suite**

`database/database_properties_test.go`:

```go
package database

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

var identGen = rapid.StringMatching(`[a-z][a-z0-9_]{0,29}`)

var (
	pgPlaceholderRe     = regexp.MustCompile(`\$(\d+)`)
	oraclePlaceholderRe = regexp.MustCompile(`:(\d+)`)
)

func buildEqChain(t *rapid.T, qb *QueryBuilder, n int) (sql string, args []any, err error) {
	f := qb.Filter()
	filter := f.Eq(identGen.Draw(t, "col0"), rapid.Int().Draw(t, "val0"))
	for i := 1; i < n; i++ {
		filter = f.And(filter, f.Eq(identGen.Draw(t, fmt.Sprintf("col%d", i)), rapid.Int().Draw(t, fmt.Sprintf("val%d", i))))
	}
	return qb.Select("id").From("users").Where(filter).ToSQL()
}

func TestQueryBuilderPlaceholderArityProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		vendor := rapid.SampledFrom([]string{PostgreSQL, Oracle}).Draw(rt, "vendor")
		n := rapid.IntRange(1, 8).Draw(rt, "conds")
		sql, args, err := buildEqChain(rt, NewQueryBuilder(vendor), n)
		if err != nil {
			rt.Fatalf("ToSQL: %v", err)
		}
		re := pgPlaceholderRe
		if vendor == Oracle {
			re = oraclePlaceholderRe
		}
		if got := len(re.FindAllString(sql, -1)); got != len(args) {
			rt.Fatalf("placeholders %d != args %d in %q", got, len(args), sql)
		}
	})
}

func TestQueryBuilderPostgresPlaceholdersSequentialProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(rt, "conds")
		sql, _, err := buildEqChain(rt, NewQueryBuilder(PostgreSQL), n)
		if err != nil {
			rt.Fatalf("ToSQL: %v", err)
		}
		for i, m := range pgPlaceholderRe.FindAllStringSubmatch(sql, -1) {
			if m[1] != strconv.Itoa(i+1) {
				rt.Fatalf("placeholder %d is $%s in %q", i+1, m[1], sql)
			}
		}
	})
}

func TestQueryBuilderDeterministicOutputProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		vendor := rapid.SampledFrom([]string{PostgreSQL, Oracle}).Draw(rt, "vendor")
		col := identGen.Draw(rt, "col")
		val := rapid.Int().Draw(rt, "val")
		build := func() (string, []any) {
			qb := NewQueryBuilder(vendor)
			sql, args, err := qb.Select("id").From("users").Where(qb.Filter().Eq(col, val)).ToSQL()
			if err != nil {
				rt.Fatalf("ToSQL: %v", err)
			}
			return sql, args
		}
		sql1, args1 := build()
		sql2, args2 := build()
		if sql1 != sql2 || fmt.Sprint(args1) != fmt.Sprint(args2) {
			rt.Fatalf("non-deterministic: %q vs %q", sql1, sql2)
		}
	})
}

func TestQueryBuilderOracleReservedWordQuotingProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		reserved := rapid.SampledFrom([]string{"number", "level", "size"}).Draw(rt, "reserved")
		sql, _, err := NewQueryBuilder(Oracle).Select("id", reserved).From("users").ToSQL()
		if err != nil {
			rt.Fatalf("ToSQL: %v", err)
		}
		if want := `"` + reserved + `"`; !strings.Contains(sql, want) {
			rt.Fatalf("reserved word %q not quoted in %q", reserved, sql)
		}
	})
}
```

- [ ] **Step 3: Run the suite**

Run: `go test -race -run 'Property' ./database/`
Expected: PASS. On failure, rapid prints a reproducing seed — a genuine invariant violation is a framework bug: STOP and report it, do not massage the property.

- [ ] **Step 4: make check, commit**

```bash
make check
git add go.mod go.sum tools/migration/go.mod tools/migration/go.sum database/database_properties_test.go
git commit -F <msgfile>   # "test(database): property suite for query builder invariants"
git log -1
```

(Include the tools/migration files only if Step 1 actually changed them.)

---

### Task 9: Config property suite

**Files:**
- Create: `config/config_properties_test.go`

**Interfaces:**
- Consumes: `Load() (*Config, error)`, `(*Config).InjectInto(target any) error`, test helper `clearEnvironmentVariables()` (already in `config` package tests).

- [ ] **Step 1: Write the suite**

Note: white-box (`package config`) to reuse `clearEnvironmentVariables()`. Env mutation uses `os.Setenv` per rapid iteration — the config package's tests are non-parallel by construction (they already use `t.Setenv`).

```go
package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestInjectIntoEnvStringRoundTripProperty(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	rapid.Check(t, func(rt *rapid.T) {
		val := rapid.StringMatching(`[!-~]{1,64}`).Draw(rt, "val") // printable ASCII, no spaces
		os.Setenv("CUSTOM_PROP_VALUE", val)
		defer os.Unsetenv("CUSTOM_PROP_VALUE")

		cfg, err := Load()
		if err != nil {
			rt.Fatalf("Load: %v", err)
		}
		var svc struct {
			Value string `config:"custom.prop.value" default:"fallback"`
		}
		if err := cfg.InjectInto(&svc); err != nil {
			rt.Fatalf("InjectInto: %v", err)
		}
		if svc.Value != val {
			rt.Fatalf("env round-trip: got %q want %q", svc.Value, val)
		}
	})
}

// Plain regression test, not a property: struct tags are compile-time, so
// there is nothing meaningful to draw.
func TestInjectIntoDefaultAppliesWhenEnvAbsent(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var svc struct {
		Value string `config:"custom.prop.value" default:"placeholder"`
	}
	if err := cfg.InjectInto(&svc); err != nil {
		t.Fatalf("InjectInto: %v", err)
	}
	if svc.Value != "placeholder" {
		t.Fatalf("default not applied: got %q", svc.Value)
	}
}

func TestInjectIntoDurationRoundTripProperty(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	rapid.Check(t, func(rt *rapid.T) {
		d := time.Duration(rapid.Int64Range(1, int64(time.Hour)).Draw(rt, "dur"))
		os.Setenv("CUSTOM_PROP_TIMEOUT", d.String())
		defer os.Unsetenv("CUSTOM_PROP_TIMEOUT")

		cfg, err := Load()
		if err != nil {
			rt.Fatalf("Load: %v", err)
		}
		var svc struct {
			Timeout time.Duration `config:"custom.prop.timeout"`
		}
		if err := cfg.InjectInto(&svc); err != nil {
			rt.Fatalf("InjectInto: %v", err)
		}
		if svc.Timeout != d {
			rt.Fatalf("duration round-trip: got %v want %v", svc.Timeout, d)
		}
	})
}

func TestInjectIntoStringSliceRoundTripProperty(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	rapid.Check(t, func(rt *rapid.T) {
		elems := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,8}`), 1, 5).Draw(rt, "elems")
		os.Setenv("CUSTOM_PROP_TAGS", strings.Join(elems, ","))
		defer os.Unsetenv("CUSTOM_PROP_TAGS")

		cfg, err := Load()
		if err != nil {
			rt.Fatalf("Load: %v", err)
		}
		var svc struct {
			Tags []string `config:"custom.prop.tags"`
		}
		if err := cfg.InjectInto(&svc); err != nil {
			rt.Fatalf("InjectInto: %v", err)
		}
		if len(svc.Tags) != len(elems) {
			rt.Fatalf("slice round-trip: got %v want %v", svc.Tags, elems)
		}
		for i := range elems {
			if svc.Tags[i] != elems[i] {
				rt.Fatalf("slice round-trip: got %v want %v", svc.Tags, elems)
			}
		}
	})
}
```

- [ ] **Step 2: Run**

Run: `go test -race -run 'InjectInto' ./config/`
Expected: PASS. A failure here is a real config bug (e.g. a value koanf mangles) — STOP and report with the rapid seed; do not shrink the generator to dodge it without recording why.

- [ ] **Step 3: make check, commit**

```bash
make check
git add config/config_properties_test.go
git commit -F <msgfile>   # "test(config): property suite for InjectInto round-trips"
git log -1
```

---

### Task 10: JOSE property suite

**Files:**
- Create: `jose/jose_properties_test.go` (package `jose_test` — white-box would cycle through `jose/testing`, which imports `jose`)

**Interfaces:**
- Consumes: `jose.Seal(payload []byte, p *jose.Policy, r jose.KeyResolver) (string, error)`, `jose.Open(compact string, p *jose.Policy, r jose.KeyResolver) ([]byte, *jose.Claims, jose.OpenHeader, error)`, `josetest.NewBidirectionalFixture(t)` (fields `ClientOutbound`, `PeerInbound`, `Resolver`).

- [ ] **Step 1: Write the suite**

```go
package jose_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/gaborage/go-bricks/jose"
	josetest "github.com/gaborage/go-bricks/jose/testing"
)

func TestSealOpenRoundTripProperty(t *testing.T) {
	fx := josetest.NewBidirectionalFixture(t) // keygen once; iterations reuse
	rapid.Check(t, func(rt *rapid.T) {
		payload := rapid.SliceOfN(rapid.Byte(), 1, 4096).Draw(rt, "payload")
		sealed, err := jose.Seal(payload, fx.ClientOutbound, fx.Resolver)
		if err != nil {
			rt.Fatalf("Seal: %v", err)
		}
		plain, _, _, err := jose.Open(sealed, fx.PeerInbound, fx.Resolver)
		if err != nil {
			rt.Fatalf("Open: %v", err)
		}
		if !bytes.Equal(plain, payload) {
			rt.Fatalf("round-trip mismatch: %d bytes in, %d out", len(payload), len(plain))
		}
	})
}

// The security invariant is NOT "any tamper errors" — a base64 trailing-bit
// swap can decode identically. It is: Open never succeeds with plaintext
// different from the original.
func TestOpenTamperNeverAltersPayloadProperty(t *testing.T) {
	fx := josetest.NewBidirectionalFixture(t)
	payload := []byte(`{"amount":"100.00","currency":"USD"}`)
	sealed, err := jose.Seal(payload, fx.ClientOutbound, fx.Resolver)
	require.NoError(t, err)

	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	rapid.Check(t, func(rt *rapid.T) {
		pos := rapid.IntRange(0, len(sealed)-1).Draw(rt, "pos")
		repl := alphabet[rapid.IntRange(0, len(alphabet)-1).Draw(rt, "chr")]
		if sealed[pos] == repl {
			return
		}
		tampered := []byte(sealed)
		tampered[pos] = repl
		plain, _, _, err := jose.Open(string(tampered), fx.PeerInbound, fx.Resolver)
		if err == nil && !bytes.Equal(plain, payload) {
			rt.Fatalf("tampered token at pos %d opened with ALTERED plaintext", pos)
		}
	})
}
```

- [ ] **Step 2: Run**

Run: `go test -race -run 'Property' ./jose/`
Expected: PASS. `TestOpenTamperNeverAltersPayloadProperty` failing is a cryptographic integrity bug — STOP and report immediately with the seed.

- [ ] **Step 3: make check, commit**

```bash
make check
git add jose/jose_properties_test.go
git commit -F <msgfile>   # "test(jose): property suite for seal/open integrity"
git log -1
```

---

### Task 11: Multitenant resolver property suite

**Files:**
- Create: `multitenant/multitenant_properties_test.go` (package `multitenant`)

**Interfaces:**
- Consumes: `TenantResolver` interface (`ResolveTenant(ctx, *http.Request) (string, error)`), struct literals `&HeaderResolver{HeaderName}`, `&SubdomainResolver{RootDomain, TrustProxies}`, `&PathResolver{Segment, Prefix}`, `&CompositeResolver{Resolvers, TenantRegex}`.

- [ ] **Step 1: Write the suite**

```go
package multitenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"pgregory.net/rapid"
)

type stubResolver struct {
	tenant string
	err    error
	called bool
}

func (s *stubResolver) ResolveTenant(_ context.Context, _ *http.Request) (string, error) {
	s.called = true
	return s.tenant, s.err
}

// Contract: resolution identifies or errors — never panics, never returns
// ("", nil). rapid surfaces any panic as a failure with a reproducing seed.
func TestResolversNeverPanicOrReturnEmptyProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		host := rapid.StringMatching(`[a-z0-9.-]{1,40}`).Draw(rt, "host")
		path := "/" + rapid.StringMatching(`[a-zA-Z0-9/._-]{0,60}`).Draw(rt, "path")
		hdr := rapid.StringMatching(`[ -~]{0,40}`).Draw(rt, "hdr")

		req := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
		req.Host = host
		req.URL.Path = path
		req.Header.Set("X-Tenant-ID", hdr)

		resolvers := []TenantResolver{
			&HeaderResolver{HeaderName: "X-Tenant-ID"},
			&SubdomainResolver{RootDomain: "example.com"},
			&PathResolver{Segment: rapid.IntRange(1, 4).Draw(rt, "seg"), Prefix: "itsp"},
			&CompositeResolver{Resolvers: []TenantResolver{
				&SubdomainResolver{RootDomain: "example.com"},
				&HeaderResolver{HeaderName: "X-Tenant-ID"},
			}},
		}
		for _, r := range resolvers {
			tenant, err := r.ResolveTenant(context.Background(), req)
			if err == nil && tenant == "" {
				rt.Fatalf("%T returned empty tenant without error", r)
			}
		}
	})
}

func TestCompositeFirstMatchProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(rt, "n")
		winner := rapid.IntRange(0, n-1).Draw(rt, "winner")
		stubs := make([]*stubResolver, n)
		chain := make([]TenantResolver, n)
		for i := range stubs {
			if i < winner {
				stubs[i] = &stubResolver{err: ErrTenantResolutionFailed}
			} else {
				stubs[i] = &stubResolver{tenant: "tenant-x"}
			}
			chain[i] = stubs[i]
		}
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		got, err := (&CompositeResolver{Resolvers: chain}).ResolveTenant(context.Background(), req)
		if err != nil {
			rt.Fatalf("composite failed with a succeeding resolver at %d: %v", winner, err)
		}
		if got != "tenant-x" {
			rt.Fatalf("got %q want tenant-x", got)
		}
		for i := winner + 1; i < n; i++ {
			if stubs[i].called {
				rt.Fatalf("resolver %d consulted after winner %d", i, winner)
			}
		}
	})
}
```

If `ErrTenantResolutionFailed` is not the exported sentinel in `multitenant/errors.go`, use the sentinel that is (check that file first). If the never-empty invariant genuinely fails for some resolver, that is a resolution-contract bug — STOP and report; do not relax the property.

- [ ] **Step 2: Run**

Run: `go test -race -run 'Property' ./multitenant/`
Expected: PASS

- [ ] **Step 3: make check, commit**

```bash
make check
git add multitenant/multitenant_properties_test.go
git commit -F <msgfile>   # "test(multitenant): property suite for resolver contracts"
git log -1
```

---

### Task 12: PR2 documentation

**Files:**
- Modify: `wiki/testing.md` (new `## Property-Based Tests` section, after the Mutation Gate section from Task 6)

- [ ] **Step 1: Append the section**

```markdown
## Property-Based Tests

Invariant-heavy packages carry a `<pkg>_properties_test.go` suite built on
[`pgregory.net/rapid`](https://pkg.go.dev/pgregory.net/rapid) — a documented
exception to the source-to-test 1:1 naming rule, alongside `testhelpers_test.go`.
Current exemplars: `database` (placeholder arity, Oracle reserved-word quoting,
determinism), `config` (InjectInto round-trips), `jose` (seal/open integrity),
`multitenant` (resolver contracts, composite first-match).

Pattern rules:

- One `rapid.Check` per invariant; name it `Test<Subject><Invariant>Property`.
- Expensive setup (key generation) lives OUTSIDE `rapid.Check`; iterations reuse it.
- State the invariant precisely. Example: tamper-resistance is not "any tamper
  errors" (base64 trailing bits can decode identically) but "Open never succeeds
  with altered plaintext".
- A failing property prints a reproducing seed (`-rapid.seed`); a genuine
  violation is a framework bug — fix the code, never the generator.

Property suites are ordinary `go test` tests: they run in `make test`, under
`-race`, and count toward coverage.
```

- [ ] **Step 2: make check, commit**

```bash
make check
git add wiki/testing.md
git commit -F <msgfile>   # "docs(testing): property-based test patterns"
git log -1
```

---

### Task 13: PR2 gates and push

**MAIN SESSION ONLY.**

- [ ] **Step 1:** `make check` green; run `make mutate` (PR1 is merged, so the gate exists — test files are excluded from mutation scope, so expect "no mutatable changes" unless the branch touched non-test code).
- [ ] **Step 2:** `/simplify` → `/security-audit` → `/code-review`, with `make check` after any gate that changes code and a CodeRabbit re-run if findings were applied.
- [ ] **Step 3:** `git push -u origin feature/property-based-suites`, open the PR:

```markdown
## What
Example-based tests can miss whole input classes. Adds pgregory.net/rapid and
four property suites pinning invariants: query builder placeholder arity /
Oracle reserved-word quoting / determinism, config InjectInto round-trips
(string, duration, []string), JOSE seal→open integrity (tampering never yields
altered plaintext), and tenant-resolver contracts (never panic, never
empty-without-error, composite first-match).

## Impact
New test-only dependency pgregory.net/rapid. `<pkg>_properties_test.go` is now
a documented naming exception; pattern rules in wiki/testing.md.

## Verification
Suites run under -race inside make test; property failures print reproducing
seeds. CI gates only beyond that.
```

- [ ] **Step 4:** `/sonar-pr <N>`; address every reviewer finding or document the skip.

---

## Self-Review Record

- Spec coverage: S1 → Tasks 1–4; S2 → Task 5; S3 → Tasks 8–11; S4 → Tasks 6 and 12; success criteria: (1) Task 4 Step 5, (2) Task 4 Step 3, (3) Task 5 (workflow_dispatch post-merge), (4) Tasks 8–11 under `-race`.
- Known deliberate deviations: none. The config default-tag check is a plain regression test, not a property (struct tags are compile-time; noted inline in Task 9).
- Engine-truth dependencies (JSON field names, flag names, per-package scoping) are all pinned to Task 1 spike evidence with STOP gates on the two unrecoverable mismatches.
