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
# Only EXPLICIT //go:build expressions are read. Go's implicit filename
# constraints (foo_darwin.go, foo_arm64.go) are invisible here; the tree's one
# GOOS-constrained file, migration/proc_windows.go, carries the explicit tag too.
set -euo pipefail

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
tags_of() { local expr="${1#*//go:build}"; echo "${expr//[()!\&|]/ }"; }

if [ "${1:-}" = "--packages-for" ]; then
  # No cd: git grep scopes to the caller's directory and prints paths relative
  # to it, which is exactly the scoping a submodule needs.
  want="${2:-}"; [ -n "$want" ] || die "--packages-for needs a tag"
  while IFS=: read -r file _line expr; do
    for tag in $(tags_of "$expr"); do
      if [ "$tag" = "$want" ]; then printf './%s\n' "$(dirname "$file")"; fi
    done
  done < <(git grep -n '^//go:build' -- '*.go') | sort -u
  exit 0
fi

ROOT="$(git rev-parse --show-toplevel)" || die "not in a git repo"
cd "$ROOT"

violations=$(
  while IFS=: read -r file _line expr; do
    for tag in $(tags_of "$expr"); do
      case " $ALLOWED " in
        *" $tag "*) ;;
        *) printf '%s: %s\n' "$file" "$tag" ;;
      esac
    done
  done < <(git grep -n '^//go:build' -- '*.go') | sort -u
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
