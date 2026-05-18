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

# Cheapest guard first: only Go source files can contain f.Raw()/jf.Raw().
# Extracting just the path keeps the common non-Go edit on a single jq fork.
file="$(jq -r '.tool_input.file_path // empty' <<<"$input" 2>/dev/null || true)"
case "$file" in
  *.go) ;;
  *) exit 0 ;;
esac

# The text this edit ADDS, across tool shapes, in one jq pass:
#   Write      -> .tool_input.content
#   Edit       -> .tool_input.new_string
#   MultiEdit  -> joined .tool_input.edits[].new_string  (nulls filtered)
added="$(jq -r '
  .tool_input.content
  // .tool_input.new_string
  // ([.tool_input.edits[]?.new_string // empty] | join("\n"))
  // ""' <<<"$input" 2>/dev/null || true)"

# Does this edit introduce a raw-SQL escape hatch?
if ! grep -Eq '(^|[^A-Za-z0-9_])(f|jf)\.Raw\(' <<<"$added"; then
  exit 0
fi

# Is the mandatory annotation present in the SAME edit?
if grep -q 'SECURITY: Manual SQL review completed' <<<"$added"; then
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
