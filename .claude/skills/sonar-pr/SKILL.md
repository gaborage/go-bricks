---
name: sonar-pr
description: >-
  Fetch and triage the SonarCloud NEW-issue list for a GoBricks pull request.
  SonarCloud's "Quality Gate passed" banner hides the per-PR issue list; this
  pulls it via the public API and triages every OPEN/CONFIRMED finding.
disable-model-invocation: true
---

# sonar-pr

Use when reviewing or fixing a PR. The argument is the PR number (e.g.
`/sonar-pr 447`). If no number is given, infer it from the current branch via
`gh pr view --json number -q .number`.

## Steps

1. Resolve the PR number `N` (from the argument, else `gh pr view`).

2. Fetch the NEW issues (public endpoint, no auth needed):

   ```bash
   curl -sS "https://sonarcloud.io/api/issues/search?componentKeys=gaborage_go-bricks&pullRequest=N&statuses=OPEN,CONFIRMED&ps=500" \
     | jq -r '.issues[] | "\(.severity)\t\(.rule)\t\(.component | sub("^gaborage_go-bricks:";""))#\(.line // 0)\t\(.message)"' \
     | sort
   ```

   Also fetch security hotspots, which the issues endpoint omits:

   ```bash
   curl -sS "https://sonarcloud.io/api/hotspots/search?projectKey=gaborage_go-bricks&pullRequest=N&ps=500" \
     | jq -r '.hotspots[]? | "HOTSPOT\t\(.securityCategory)\t\(.component | sub("^gaborage_go-bricks:";""))#\(.line // 0)\t\(.message)"'
   ```

3. Triage every finding. Apply the CLAUDE.md standard: this is **all-or-nothing**
   — fix each MAJOR/MINOR/BLOCKER or document the skip in the commit message.
   For framework-precedent rules (S8179 getter naming, S8196 interface naming,
   etc.) the precedent is **rename/refactor, never nolint** (see ADR-013).
   Note known false positives already dispositioned in SonarCloud (e.g. the 6
   S8168 Begin/BeginTx transaction-factory issues) and do not re-flag them.

4. Output a table grouped by severity:

   ```
   ## SonarCloud — PR #N  (X issues, Y hotspots)

   | Severity | Rule | Location | Message | Disposition |
   |----------|------|----------|---------|-------------|
   ```

   `Disposition` = `fix` | `skip: <reason>` | `false-positive: <ref>`.

5. End with a one-line verdict: how many require code changes before the PR is
   merge-ready, and whether any need a documented skip in the commit message.
