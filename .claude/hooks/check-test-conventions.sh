#!/usr/bin/env bash
# PostToolUse linter for GoBricks *_test.go files.
#
# Enforces two strict, frequently-violated CLAUDE.md conventions that
# otherwise only get caught by a SonarCloud / CodeRabbit round-trip:
#
#   1. camelCase test function names (snake_case forbidden; 100% compliance).
#   2. Source-to-test 1:1 file naming (no foo_extra_test.go / foo_uncovered_test.go;
#      testhelpers_test.go is the documented shared-helper exception).
#
# The file is already written (PostToolUse), so this cannot block — it exits 2
# with a message on stderr, which is fed back to Claude so it self-corrects in
# the same turn. No violation -> silent exit 0 (no noise on every test edit).
set -uo pipefail

# Can't lint without jq; PostToolUse can't block anyway, so warn (for
# debuggability) and exit 0 rather than nagging on every edit.
command -v jq >/dev/null 2>&1 || {
  echo "check-test-conventions: jq not found — test-convention linting skipped." >&2
  exit 0
}

input="$(cat)"
file="$(jq -r '.tool_input.file_path // empty' <<<"$input" 2>/dev/null || true)"

case "$file" in
  *_test.go) ;;
  *) exit 0 ;;
esac

[ -f "$file" ] || exit 0

problems=""
base="$(basename -- "$file")"

case "$base" in
  testhelpers_test.go)
    : # documented shared-helper exception — allowed
    ;;
  *_extra_test.go|*_uncovered_test.go|*_additional_test.go|*_more_test.go|*_extra2_test.go|*_uncovered2_test.go)
    problems+="- Filename '$base' violates source-to-test 1:1 naming."$'\n'
    problems+="  'foo.go' has exactly one companion 'foo_test.go'. Append gap-closing"$'\n'
    problems+="  tests to the existing companion instead of creating a new *_extra/_uncovered file."$'\n'
    ;;
esac

# Snake_case in Test/Benchmark/Fuzz function names is forbidden. The optional
# (\[[^]]*\])? tolerates a Go 1.25 generic type-param list before the args,
# e.g. func TestFoo_Bar[T any](...). Example functions are intentionally
# excluded: Go's ExampleFoo_method form legitimately uses underscores.
bad_funcs="$(grep -nE '^func[[:space:]]+(Test|Benchmark|Fuzz)[A-Za-z0-9]*_[A-Za-z0-9_]*(\[[^]]*\])?\(' -- "$file" || true)"
if [ -n "$bad_funcs" ]; then
  problems+="- Snake_case test function name(s) found (camelCase is MANDATORY):"$'\n'
  while IFS= read -r line; do
    problems+="    $line"$'\n'
  done <<<"$bad_funcs"
  problems+="  Rename e.g. TestUserService_CreateUser -> TestUserServiceCreateUser."$'\n'
  problems+="  (Table-driven test CASE names still use snake_case — only func names are camelCase.)"$'\n'
fi

if [ -n "$problems" ]; then
  printf 'GoBricks test-convention violations in %s:\n\n' "$file" >&2
  printf '%s' "$problems" >&2
  printf '\nFix before continuing — see CLAUDE.md "Testing Conventions".\n' >&2
  exit 2
fi
exit 0
