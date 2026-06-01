export const meta = {
  name: 'doc-drift-fix',
  description: 'Apply confirmed documentation & comment drift fixes, one editor agent per file (disjoint files, conflict-free)',
  phases: [
    { title: 'Load', detail: 'load the list of files with confirmed findings' },
    { title: 'Fix', detail: 'one editor agent per file applies + re-verifies fixes' },
  ],
}

const FIX_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: {
    file: { type: 'string' },
    appliedCount: { type: 'integer', description: 'number of findings actually applied' },
    skipped: {
      type: 'array',
      items: {
        type: 'object',
        additionalProperties: false,
        properties: { detail: { type: 'string', description: 'which finding was skipped and why' } },
        required: ['detail'],
      },
    },
    notes: { type: 'string', description: 'anything notable, e.g. multi-occurrence edits or build risks' },
  },
  required: ['file', 'appliedCount', 'skipped', 'notes'],
}

const LOAD_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: { files: { type: 'array', items: { type: 'string' } } },
  required: ['files'],
}

function fixPrompt(file) {
  return `You are fixing documentation / code-comment drift in ONE file: \`${file}\`.
Edit ONLY this file. Do NOT run git, make, go build, or any tests.

STEP 1 — Load the confirmed findings for this file:
  jq -c --arg f '${file}' '.[] | select(.file==$f)' /tmp/doc_findings.json
  jq -c --arg f '${file}' '.[] | select(.file==$f)' /tmp/comment_findings.json
Each finding has: line, severity, kind, claim, reality, evidence, suggestedFix, confidence.

STEP 2 — For EACH finding:
  a. Read the cited region of \`${file}\` (line numbers may have shifted slightly; locate by content).
  b. RE-VERIFY against the CURRENT code: read the source location named in "evidence" (use rg / grep / Read) and confirm the drift is real right now.
  c. If confirmed, apply "suggestedFix" with the Edit tool — minimal, surgical, matching surrounding style.
  d. If the finding is wrong, already fixed, or risky, SKIP it and record the reason.

CONSERVATIVE COMMENT RULES (.go files): only remove or rewrite a comment when it is genuinely (1) stale/contradictory to the code, (2) commented-out code, or (3) pure narration that restates the adjacent line. NEVER remove — fix the text instead if stale, never delete:
  - doc comments on EXPORTED identifiers (staticcheck/godoc require them),
  - //go:build, build tags, //go:generate, //go:embed, //nolint..., //export, //line directives,
  - "// SECURITY:" annotations,
  - comments that explain WHY (rationale, trade-offs, gotchas, ADR/issue links).
  When in doubt, SKIP. Do not change code logic — comments only (plus, for the rare doc-comment-as-example case, keep it compilable).

DOC RULES (.md/.txt): apply the corrected text; keep all code examples COMPILABLE and consistent with the doc's existing style. For ADR files (wiki/adr-*.md) do NOT rewrite historical decisions — only apply the specific corrective the finding asks for (fix a wrong Status/cross-reference, or add a "Superseded by …" note).

Make NO unrelated changes. Then return: file, appliedCount, skipped (one {detail} per skipped finding with the reason), notes.`
}

phase('Load')
const loaded = await agent(
  `Run \`jq -r '.[]' /tmp/fix_files.json\` and return the resulting list of file paths as "files".`,
  { label: 'load-files', phase: 'Load', schema: LOAD_SCHEMA, agentType: 'general-purpose', model: 'sonnet' },
)

const files = (loaded && loaded.files) || []
log(`Applying fixes across ${files.length} files (one editor agent each).`)

phase('Fix')
const results = await parallel(
  files.map((f) => () =>
    agent(fixPrompt(f), { label: `fix:${f}`, phase: 'Fix', schema: FIX_SCHEMA, agentType: 'general-purpose', model: 'sonnet' })),
)

const ok = results.filter(Boolean)
const totalApplied = ok.reduce((n, r) => n + (r.appliedCount || 0), 0)
const totalSkipped = ok.reduce((n, r) => n + ((r.skipped && r.skipped.length) || 0), 0)
const skippedDetails = ok.flatMap((r) => (r.skipped || []).map((s) => `${r.file}: ${s.detail}`))
const failedFiles = files.filter((_, i) => !results[i])

log(`Applied ${totalApplied} fixes; skipped ${totalSkipped}; ${failedFiles.length} file-agents failed.`)

return {
  totalApplied,
  totalSkipped,
  filesProcessed: ok.length,
  failedFiles,
  skippedDetails,
  perFile: ok.map((r) => ({ file: r.file, applied: r.appliedCount, skipped: (r.skipped || []).length, notes: r.notes })),
}
