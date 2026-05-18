#!/usr/bin/env bash
# PreToolUse guard for GoBricks.
#
# Blocks any Edit/Write/MultiEdit that introduces an f.Raw() or jf.Raw()
# raw-SQL escape hatch WITHOUT the mandatory inline SECURITY annotation
# required by CLAUDE.md ("Security Guidelines" / Developer Manifesto).
#
# Rationale: the annotation is a forcing function for manual SQL review and
# keeps every call site grep-discoverable. A pre-edit block is strictly
# better than relying on a human remembering to grep later.
#
# Scope note: detection mirrors the canonical discovery grep in CLAUDE.md
# (git grep -E 'f\.Raw\(|jf\.Raw\(') on purpose — it is tied to the documented
# convention, not a general static analyzer. Receivers not named f/jf are out
# of scope by design (the same as the CLAUDE.md grep).
#
# Fails CLOSED: a missing dependency or unparseable input blocks rather than
# silently allowing an unguarded edit.
#
# Exit codes: 0 = allow, 2 = block (stderr is shown to Claude).
set -uo pipefail

command -v jq >/dev/null 2>&1 || {
  echo "check-raw-sql: jq not found — refusing to allow a potentially unguarded edit." >&2
  exit 2
}

input="$(cat)"

block_unparseable() {
  echo "check-raw-sql: unparseable or unsupported hook payload — refusing to allow a potentially unguarded edit." >&2
  exit 2
}

# Fail CLOSED on malformed input: a payload jq cannot parse must block, never
# fall through as "not a Go file". (Swallowing jq errors with `|| true` would
# silently break the fail-closed contract this guard advertises.)
jq -e . >/dev/null 2>&1 <<<"$input" || block_unparseable

# Cheapest guard first: only Go source files can contain f.Raw()/jf.Raw().
# Extracting just the path keeps the common non-Go edit on a single jq fork.
file="$(jq -er '.tool_input.file_path // empty' <<<"$input" 2>/dev/null)" || block_unparseable
case "$file" in
  *.go) ;;
  *) exit 0 ;;
esac

# The text this edit ADDS, across tool shapes, in one jq pass:
#   Write      -> .tool_input.content
#   Edit       -> .tool_input.new_string
#   MultiEdit  -> joined .tool_input.edits[].new_string  (nulls filtered)
added="$(jq -er '
  .tool_input.content
  // .tool_input.new_string
  // ([.tool_input.edits[]?.new_string // empty] | join("\n"))
  // ""' <<<"$input" 2>/dev/null)" || block_unparseable

# Does this edit introduce a raw-SQL escape hatch?
if ! grep -Eq '(^|[^A-Za-z0-9_])(f|jf)\.Raw\(' <<<"$added"; then
  exit 0
fi

# Is the mandatory annotation present AND positioned to cover the calls?
#
# Stronger than "annotation appears anywhere in the blob": every (f|jf).Raw(
# occurrence must be preceded (same line or any earlier line in this edit) by
# a SECURITY annotation. This rejects the "annotation pasted after the calls"
# and "bare call, no annotation" cases.
#
# It deliberately does NOT enforce strict line-N-1 adjacency: CLAUDE.md /
# the security-audit policy explicitly permit ONE annotation above an
# enclosing dispatch (table-driven loop, multi-call JoinOn chain) covering
# several Raw calls. A pre-edit hook only sees a string fragment, so strict
# per-call adjacency would false-block that sanctioned pattern. Semantic
# adjacency review remains the job of /security-audit + human review.
if awk '
  /SECURITY: Manual SQL review completed/ { seen = 1 }
  /(^|[^A-Za-z0-9_])(f|jf)\.Raw\(/      { if (!seen) { missing = 1 } }
  END                                    { exit (missing ? 1 : 0) }
' <<<"$added"; then
  exit 0
fi

cat >&2 <<'EOF'
BLOCKED — raw-SQL escape hatch added without its mandatory SECURITY annotation.

CLAUDE.md requires, at EVERY f.Raw()/jf.Raw() call site, an adjacent comment:

    // SECURITY: Manual SQL review completed - <what was verified>

The rationale must name the specific property checked, e.g.:
  - identifier quoting for vendor reserved words
  - parameterization of the value side
  - absence of user-input concatenation

Re-issue this edit with the annotation included in the same new_string /
content so the call site is review-forced and grep-discoverable.

(If you are editing a file that already has Raw + annotation elsewhere and
this edit legitimately does not add a new Raw call, include the annotation
line in the edit anyway, or make the change via a hunk that excludes Raw.)
EOF
exit 2
