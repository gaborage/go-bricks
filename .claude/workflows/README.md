# Claude Code workflow scripts

The `.js` files in this directory are **Claude Code Workflow-tool scripts**,
not standalone JavaScript. The dialect uses an ESM `export const meta = {…}`
header plus **top-level `return`** and top-level `await` — the Workflow
runtime wraps the script body in an async function before executing it, and
injects orchestration globals (`agent()`, `parallel()`, `pipeline()`,
`phase()`, `log()`). Run them via the Workflow tool by name (e.g.
`Workflow({name: "doc-drift"})` or the `/doc-drift` skill), never with
`node`.

## These files are not parseable as standard JavaScript — by design

Standard parsers reject top-level `return` outside a function. Reproduce with
a module-mode syntax check reading the file on stdin:

```bash
node --input-type=module --check < .claude/workflows/doc-drift.js
# [stdin]:298
# return {
# ^^^^^^
# SyntaxError: Illegal return statement
```

Note that `node --check <file>` alone does NOT reproduce it and exits 0 —
`--check` on a file path parses as CommonJS, and Node wraps CommonJS modules
in a function, which makes top-level `return` legal there. Consequences:

- **Do not** add these files to any JS toolchain (ESLint, tsc, bundlers,
  CodeQL javascript-typescript analysis). They will always fail to parse.
- **Do not** "fix" the syntax to appease a scanner — the dialect is what the
  Workflow runtime executes; wrapping the body in a function or removing the
  top-level `return` breaks the scripts.
- A `.gitattributes` rule (`.claude/workflows/*.js linguist-detectable=false`)
  keeps these files out of GitHub language detection so the repo is not
  classified as containing JavaScript. New workflow scripts added here are
  covered automatically by the glob.

## Incident record (June 2026)

When the first scripts landed (PR #505, 2026-05-31), GitHub language
detection began reporting JavaScript, and CodeQL **default setup**
(GitHub-managed, no workflow file in this repo) automatically added an
`Analyze (javascript-typescript)` job. With zero parseable JS in the repo,
the extractor failed every run (exit 32, "no source code seen"):
scheduled scan on `main` 2026-06-01
(actions/runs/26734254677) and PR #572 2026-06-10. Resolution, in layers:

1. **Settings**: default setup was pinned to an explicit language list.
   That state lives in GitHub settings only — the current list and the date
   it was set are recorded in `wiki/troubleshooting.md` ("CI/CD Issues");
   inspect live with
   `gh api repos/gaborage/go-bricks/code-scanning/default-setup`.
2. **This repo**: the `.gitattributes` rule above is intended to keep GitHub
   language detection from classifying the repo as JavaScript, so a future
   default-setup reset to automatic detection should not resurrect the
   failing job. (The effect shows up only after GitHub recomputes language
   stats, which happens asynchronously and may take more than one
   default-branch push.)

If CodeQL javascript-typescript analysis is ever wanted again (i.e. real
JavaScript/TypeScript enters the repo), it will only succeed if at least one
standard-grammar JS/TS file exists — the workflow scripts will still be
per-file extraction errors, which CodeQL tolerates when other files parse.
