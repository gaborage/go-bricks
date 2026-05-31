export const meta = {
  name: 'doc-drift',
  description: 'Detect documentation drift (docs vs current code) and code-comment drift across the repo; emit a ranked markdown drift report',
  whenToUse: 'Run before a release, after large refactors, or periodically to find docs/comments that no longer match the code. Read-only: produces a report, never edits.',
  phases: [
    { title: 'Discover', detail: 'enumerate tracked docs + non-test Go files via git' },
    { title: 'Docs: review', detail: 'extract + verify concrete claims per doc cluster' },
    { title: 'Docs: verify', detail: 'adversarially confirm candidate doc findings' },
    { title: 'Comments: review', detail: 'flag stale / redundant comments per code cluster' },
    { title: 'Comments: verify', detail: 'adversarially confirm candidate comment findings' },
  ],
}

// ---------------------------------------------------------------------------
// Schemas
// ---------------------------------------------------------------------------

// A single drift finding. Shared by review and verify stages so the verifier
// can return the same shape (minus the rejected items).
const FINDING_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: {
    findings: {
      type: 'array',
      items: {
        type: 'object',
        additionalProperties: false,
        properties: {
          file: { type: 'string', description: 'doc or source file path the finding is about' },
          line: { type: 'integer', description: 'line number in `file`; 0 if not applicable' },
          severity: { type: 'string', enum: ['high', 'medium', 'low'] },
          kind: { type: 'string', description: 'short tag, e.g. stale-api, wrong-default, removed-feature, broken-example, superseded-adr, stale-comment, commented-out-code, redundant-narration' },
          claim: { type: 'string', description: 'what the doc/comment asserts' },
          reality: { type: 'string', description: 'what the current code actually does' },
          evidence: { type: 'string', description: 'the source location(s) (file:line) proving `reality`' },
          suggestedFix: { type: 'string', description: 'concrete fix: the corrected text, or "delete"' },
          confidence: { type: 'string', enum: ['high', 'medium', 'low'] },
        },
        required: ['file', 'line', 'severity', 'kind', 'claim', 'reality', 'evidence', 'suggestedFix', 'confidence'],
      },
    },
  },
  required: ['findings'],
}

const DISCOVERY_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  properties: {
    docs: {
      type: 'array',
      items: {
        type: 'object',
        additionalProperties: false,
        properties: {
          path: { type: 'string' },
          lines: { type: 'integer' },
          lens: { type: 'string', enum: ['living', 'adr', 'index'] },
        },
        required: ['path', 'lines', 'lens'],
      },
    },
    codeFiles: {
      type: 'array',
      items: { type: 'string' },
      description: 'tracked non-test .go file paths',
    },
  },
  required: ['docs', 'codeFiles'],
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function chunk(arr, n) {
  const out = []
  for (let i = 0; i < arr.length; i += n) out.push(arr.slice(i, i + n))
  return out
}

function mkLiving(docs) {
  return { type: 'doc', lens: 'living', label: docs.map((d) => d.path).join(', '), files: docs.map((d) => d.path) }
}

// Build doc clusters with per-lens batching. llms.txt is huge and example-heavy
// so it is chunked by line range; big living docs get their own agent; small
// ones are batched; ADRs are batched 3-per-agent under the superseded lens.
function buildDocClusters(docs) {
  const clusters = []
  const llms = docs.find((d) => d.path === 'llms.txt')
  const living = docs.filter((d) => d.lens === 'living' && d.path !== 'llms.txt')
  const adrs = docs.filter((d) => d.lens === 'adr')
  const index = docs.filter((d) => d.lens === 'index')

  if (llms) {
    const size = 1300
    const n = Math.max(1, Math.ceil(llms.lines / size))
    for (let i = 0; i < n; i++) {
      clusters.push({
        type: 'doc', lens: 'living', label: `llms.txt#${i + 1}/${n}`, files: [llms.path],
        range: [i * size + 1, Math.min((i + 1) * size, llms.lines)],
      })
    }
  }

  const small = []
  for (const d of living.slice().sort((a, b) => b.lines - a.lines)) {
    if (d.lines >= 250) clusters.push({ type: 'doc', lens: 'living', label: d.path, files: [d.path] })
    else small.push(d)
  }
  let cur = [], curLines = 0
  for (const d of small) {
    if (cur.length >= 3 || curLines + d.lines > 400) {
      if (cur.length) { clusters.push(mkLiving(cur)); cur = []; curLines = 0 }
    }
    cur.push(d); curLines += d.lines
  }
  if (cur.length) clusters.push(mkLiving(cur))

  for (const d of index) clusters.push({ type: 'doc', lens: 'index', label: d.path, files: [d.path] })
  chunk(adrs.map((d) => d.path), 3).forEach((files, i) => clusters.push({ type: 'doc', lens: 'adr', label: `adrs#${i + 1}`, files }))

  return clusters
}

const SEV_RANK = { high: 0, medium: 1, low: 2 }

function renderReport(docFindings, commentFindings) {
  const all = [...docFindings, ...commentFindings]
  const bySev = (arr) => ({
    high: arr.filter((f) => f.severity === 'high').length,
    medium: arr.filter((f) => f.severity === 'medium').length,
    low: arr.filter((f) => f.severity === 'low').length,
  })
  const d = bySev(docFindings)
  const c = bySev(commentFindings)

  const lines = []
  lines.push('# Documentation & Comment Drift Report')
  lines.push('')
  lines.push('> Generated by the `doc-drift` workflow. Each finding cites the source location that contradicts the doc/comment. Verify before applying.')
  lines.push('')
  lines.push('## Summary')
  lines.push('')
  lines.push('| Category | High | Medium | Low | Total |')
  lines.push('|---|---:|---:|---:|---:|')
  lines.push(`| Documentation drift | ${d.high} | ${d.medium} | ${d.low} | ${docFindings.length} |`)
  lines.push(`| Code-comment drift | ${c.high} | ${c.medium} | ${c.low} | ${commentFindings.length} |`)
  lines.push(`| **All** | **${d.high + c.high}** | **${d.medium + c.medium}** | **${d.low + c.low}** | **${all.length}** |`)
  lines.push('')

  const section = (title, findings) => {
    lines.push(`## ${title}`)
    lines.push('')
    if (!findings.length) { lines.push('_No findings._'); lines.push(''); return }
    const sorted = findings.slice().sort((a, b) =>
      (SEV_RANK[a.severity] - SEV_RANK[b.severity]) || a.file.localeCompare(b.file) || (a.line - b.line))
    let curFile = null
    for (const f of sorted) {
      if (f.file !== curFile) { curFile = f.file; lines.push(`### \`${f.file}\``); lines.push('') }
      const loc = f.line ? `:${f.line}` : ''
      lines.push(`- **[${f.severity.toUpperCase()}] ${f.kind}** (\`${f.file}${loc}\`, confidence: ${f.confidence})`)
      lines.push(`  - **Claims:** ${f.claim}`)
      lines.push(`  - **Reality:** ${f.reality}`)
      lines.push(`  - **Evidence:** ${f.evidence}`)
      lines.push(`  - **Fix:** ${f.suggestedFix}`)
    }
    lines.push('')
  }

  section('Documentation drift', docFindings)
  section('Code-comment drift', commentFindings)
  return lines.join('\n')
}

// ---------------------------------------------------------------------------
// Prompt builders
// ---------------------------------------------------------------------------

const FINDING_FIELDS_NOTE = `For each finding provide: file, line (0 if N/A), severity (high = actively misleading / will break a reader who follows it; medium = incorrect but recoverable; low = cosmetic/minor staleness), a short kind tag, the claim, the reality, evidence as a source file:line, a concrete suggestedFix (the corrected text or "delete"), and your confidence. Every finding MUST cite a specific code location in evidence — if you cannot point at code, do not report it.`

function reviewDocPrompt(cluster) {
  const fileList = cluster.files.join(', ')
  const rangeNote = cluster.range ? `\n\nIMPORTANT: Only analyze lines ${cluster.range[0]}-${cluster.range[1]} of ${cluster.files[0]} (read that range with the Read tool's offset/limit).` : ''

  if (cluster.lens === 'adr') {
    return `You are auditing Architecture Decision Records for drift: ${fileList}.

ADRs are POINT-IN-TIME records. Describing an old/superseded approach is CORRECT, not drift — do NOT flag an ADR merely for documenting the old way.

Report a finding ONLY when:
(a) the decision recorded here has been silently REVERSED or SUPERSEDED by later code or a later ADR, yet this ADR is NOT marked superseded/amended; or
(b) the ADR's Status line, ADR number, or cross-references are factually wrong (e.g. links to the wrong ADR, claims "Accepted" for something never implemented); or
(c) the ADR is written as living present-tense guidance and that guidance is now false against current code.

Use rg/grep and read the relevant source to confirm. ${FINDING_FIELDS_NOTE}`
  }

  if (cluster.lens === 'index') {
    return `You are auditing the ADR index file: ${fileList}, against the actual ADR files in wiki/.

Run \`ls wiki/adr-*.md\` and read the index. Report findings where: an ADR file exists but is missing from the index; the index lists an ADR that does not exist; a title or number in the index disagrees with the ADR file's own heading; or the index has numbering gaps/duplicates. (This index has fallen out of sync before, so be thorough.) ${FINDING_FIELDS_NOTE}`
  }

  // living
  return `You are auditing project documentation for DRIFT against the current code implementation: ${fileList}.${rangeNote}

Read the doc, then for every CONCRETE, VERIFIABLE claim — function/method/type signatures, struct field names & tags, config keys and their default values, CLI flags, environment variables, file/package paths, version numbers (e.g. Go version in go.mod), documented defaults/behavior, and especially CODE EXAMPLES — verify it against the ACTUAL current code with rg/grep and by reading the source.

Report a finding ONLY when the doc contradicts the code: stale or wrong signature, renamed/removed symbol, wrong default value, moved path, a code example that would not compile or uses a removed/renamed API, an outdated version number, or a documented feature that no longer exists.

Do NOT report: prose/style preferences, intentionally simplified examples that are still conceptually correct, clearly-labeled roadmap/future notes, or anything you cannot tie to a specific code location. Be skeptical and precise. ${FINDING_FIELDS_NOTE}`
}

function reviewCommentPrompt(cluster) {
  const fileList = cluster.files.join(', ')
  return `You are auditing CODE COMMENTS in these Go source files for drift and for violations of the project's bare-minimum-comment philosophy:
${fileList}

Read each file. Flag a comment when it is:
1. STALE / CONTRADICTORY — describes behavior, parameters, return values, or logic that the adjacent code no longer matches (renamed symbols, changed defaults, removed branches, wrong parameter names, references to deleted types/functions/packages such as MongoDB, GetX() getters, or ToSql()).
2. COMMENTED-OUT CODE — blocks of real code left as comments.
3. REDUNDANT WHAT-NARRATION — a comment that merely restates what the immediately adjacent line obviously does (e.g. "// increment counter" above counter++, "// return nil" above return nil, or a doc comment that just re-says the signature in words).

You MUST PRESERVE — never flag these for removal (only flag them if STALE/CONTRADICTORY):
- Doc comments on EXPORTED identifiers (Go convention + staticcheck require them).
- Build/tool directives: //go:build, build tags, //go:generate, //go:embed, //nolint..., //export, //line, //go:noinline, etc.
- "// SECURITY:" annotations (mandated by project policy on f.Raw()/jf.Raw() call sites).
- Comments that explain WHY — rationale, trade-offs, non-obvious gotchas, links to ADRs/issues/specs.

Be CONSERVATIVE: when unsure whether a comment earns its place, do NOT flag it. kind should be one of: stale-comment, commented-out-code, redundant-narration. suggestedFix is usually "delete" or the rewritten comment text. ${FINDING_FIELDS_NOTE}`
}

function verifyPrompt(findings, kind) {
  return `You are an ADVERSARIAL verifier. Below are CANDIDATE ${kind} drift findings from another agent. Independently re-check EACH one by reading the cited locations in the actual repo.

REJECT (drop) any finding that is: a false positive; an illustrative-but-still-correct example; a required exported godoc mis-flagged as "redundant"; a build/tool directive; a SECURITY annotation; a why-rationale comment; an ADR correctly describing history; or anything whose stated "reality" you cannot confirm in the current code.

For findings you CONFIRM, keep them — correcting severity, line, evidence, or suggestedFix if the candidate got them wrong. Return ONLY the confirmed findings (same schema). Default to REJECTION when uncertain: a trustworthy, low-noise report matters more than catching every last item.

CANDIDATES (JSON):
${JSON.stringify(findings, null, 2)}`
}

// ---------------------------------------------------------------------------
// Stages
// ---------------------------------------------------------------------------

const reviewDocStage = (cluster) =>
  agent(reviewDocPrompt(cluster), { label: `doc:${cluster.label}`, phase: 'Docs: review', schema: FINDING_SCHEMA, model: 'sonnet' })

const reviewCommentStage = (cluster) =>
  agent(reviewCommentPrompt(cluster), { label: `code:${cluster.label}`, phase: 'Comments: review', schema: FINDING_SCHEMA, model: 'sonnet' })

function makeVerifyStage(kind, phaseName) {
  return (review, cluster) => {
    const findings = (review && review.findings) || []
    if (!findings.length) return { findings: [] }
    return agent(verifyPrompt(findings, kind), { label: `verify:${cluster.label}`, phase: phaseName, schema: FINDING_SCHEMA, model: 'sonnet' })
  }
}

// ---------------------------------------------------------------------------
// Orchestration
// ---------------------------------------------------------------------------

phase('Discover')
const inv = await agent(
  `Enumerate this repository's tracked documentation and Go source for a drift analysis. Use git, wc, and ls. Return structured data.

DOCS: list tracked doc files with \`git ls-files | grep -E '\\.(md|txt)$'\`. EXCLUDE anything under \`.claude/\` or \`docs/superpowers/\` (those are tooling/spec artifacts, not project docs) and EXCLUDE \`WARP.md\` (it is a symlink to CLAUDE.md). For each remaining file: get its line count with \`wc -l\`, and classify lens — "adr" if the path matches wiki/adr-*.md, "index" if the path is exactly wiki/architecture_decisions.md, otherwise "living".

CODE: list tracked non-test Go files with \`git ls-files '*.go' | grep -v '_test\\.go$'\`. Return them all in codeFiles.

Return docs (path, lines, lens) and codeFiles.`,
  { label: 'discover', phase: 'Discover', schema: DISCOVERY_SCHEMA, model: 'sonnet' },
)

const docClusters = buildDocClusters(inv.docs)
const codeClusters = chunk(inv.codeFiles.slice().sort(), 6).map((files, i) => ({ type: 'comment', label: `comments#${i + 1}`, files }))

log(`Discovered ${inv.docs.length} docs -> ${docClusters.length} doc clusters; ${inv.codeFiles.length} Go files -> ${codeClusters.length} comment clusters.`)

const [docResults, codeResults] = await Promise.all([
  pipeline(docClusters, reviewDocStage, makeVerifyStage('documentation', 'Docs: verify')),
  pipeline(codeClusters, reviewCommentStage, makeVerifyStage('code-comment', 'Comments: verify')),
])

const docFindings = docResults.filter(Boolean).flatMap((r) => (r && r.findings) || [])
const commentFindings = codeResults.filter(Boolean).flatMap((r) => (r && r.findings) || [])

log(`Confirmed ${docFindings.length} documentation findings and ${commentFindings.length} comment findings.`)

const markdown = renderReport(docFindings, commentFindings)

return {
  markdown,
  docFindings,
  commentFindings,
  stats: {
    docs: inv.docs.length,
    codeFiles: inv.codeFiles.length,
    docClusters: docClusters.length,
    codeClusters: codeClusters.length,
    docFindings: docFindings.length,
    commentFindings: commentFindings.length,
  },
}
