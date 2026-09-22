#!/usr/bin/env bash
# Build-tag tooling for the closed tag class (#1757). Two jobs, one tokenizer:
#
#   (no args)             guard: fail when a tag appears that no pass reads.
#                         Prints `path: tag` per violation, exits 1; silent 0.
#   --packages-for <tag>  print the package directory of every tracked .go file
#                         whose //go:build expression mentions <tag>, negated or
#                         not, one `./path` per line, relative to the CALLER's
#                         directory — so a submodule scopes the query simply by
#                         running it from its own root. Empty output is a valid
#                         answer the caller must handle; see wiki/linting.md for
#                         why an empty package list is not "nothing to do".
#
# Only EXPLICIT constraints are read. Go's implicit filename constraints
# (foo_darwin.go, foo_arm64.go) are invisible here; the tree's one GOOS-constrained
# file, migration/proc_windows.go, carries the explicit tag too. Tracked .go and .s
# sources only: cgo .c/.h and UNTRACKED files are not scanned, since git grep reads
# tracked content (CI commits everything; locally, `git add -N` first).
#
# go/build TRIMS the line before recognising a constraint, so an INDENTED
# `//go:build` is live and the scan must be anchored loosely enough to see it.
# A legacy `// +build` line with no //go:build sibling is still honoured by Go and
# is refused outright rather than parsed.
# -f disables pathname expansion. Both loops below word-split tags_of's output on
# purpose, and that split otherwise GLOBS: a tag carrying `*` or `?` is replaced by
# matching filenames from the cwd, so a crafted name could expand an unknown tag
# into an allowlisted one and the guard would pass having read nothing.
set -euo pipefail -f

die() { echo "ERROR: $*" >&2; exit 1; }

# One entry per tag, each naming the pass that reads it. A NEGATED occurrence
# still requires its base name here: `!x` implies an `x` side somewhere that
# needs a pass of its own, so the guard asks for the entry either way.
#   integration — golangci `run.build-tags: [integration]` (make lint, both CI
#                 lint jobs) and gosec invocation 1 (`-tags integration`).
#   race        — make lint-race and gosec invocation 2 (`-tags integration,race`),
#                 both over --packages-for race.
#   windows     — CI's `GOOS: windows` golangci pass and gosec invocation 3
#                 (`GOOS=windows`). The `!windows` side is the default build.
ALLOWED="integration race windows"

# tags_of <//go:build line> — the identifiers in one expression, space separated.
# Operators and parentheses are separators; `!` binds to its term, and a negated
# tag is the same tag to both callers.
# The CR strip is load-bearing, not cosmetic: on a CRLF checkout the last token
# carries a trailing \r, so `race\r` != `race` and --packages-for silently drops
# the file — the race passes would then scan nothing and still exit 0.
tags_of() { local expr="${1#*//go:build}"; expr="${expr//$'\r'/}"; echo "${expr//[()!\&|]/ }"; }

if [ "${1:-}" = "--packages-for" ]; then
  # No cd: git grep scopes to the caller's directory and prints paths relative
  # to it, which is exactly the scoping a submodule needs.
  want="${2:-}"; [ -n "$want" ] || die "--packages-for needs a tag"
  while IFS=: read -r file _line expr; do
    for tag in $(tags_of "$expr"); do
      if [ "$tag" = "$want" ]; then printf './%s\n' "$(dirname "$file")"; fi
    done
  done < <(git grep -nE '^[[:space:]]*//go:build' -- '*.go' '*.s') | sort -u
  exit 0
fi

ROOT="$(git rev-parse --show-toplevel)" || die "not in a git repo"
cd "$ROOT"

violations=$(
  while IFS=: read -r file _line expr; do
    case "$expr" in
      *'// +build'*)
        # Only an UNPAIRED +build is a coverage gap. The dual form (both lines,
        # the Go 1.17 migration shape gofmt still preserves) is legal and its
        # //go:build sibling is parsed below, so flagging it would fail a legal
        # file for style.
        if ! git grep -qE '^[[:space:]]*//go:build' -- "$file"; then
          printf '%s: legacy +build constraint with no //go:build sibling\n' "$file"
        fi
        continue
        ;;
    esac
    for tag in $(tags_of "$expr"); do
      case " $ALLOWED " in
        *" $tag "*) ;;
        *) printf '%s: %s\n' "$file" "$tag" ;;
      esac
    done
  done < <(git grep -nE '^[[:space:]]*(//go:build|// \+build)' -- '*.go' '*.s') | sort -u
)

[ -n "$violations" ] || exit 0

printf '%s\n' "$violations"
cat >&2 <<'MSG'

check-build-tags: a build tag appeared that no lint or security pass reads.
A file behind it is invisible to every gate, and a gate that reads nothing
still exits 0. Do one of:
  - add a pass that reads the new tag's side (see wiki/linting.md), or
  - add the tag to ALLOWED in this script with a comment naming that pass.
MSG
exit 1
