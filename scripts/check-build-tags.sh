#!/usr/bin/env bash
# Build-tag tooling for the closed tag class (#1757).
#
# A file behind a build tag is invisible to every pass that does not name that
# tag, and a gate that reads nothing still exits 0. Two jobs, one tokenizer:
#
#   (no args)              guard: fail when a tag appears that no pass reads.
#                          Prints `path: tag` per violation, exits 1; silent 0.
#   --packages-for <tag> [pathspec...]
#                          print the package directory of every tracked .go file
#                          whose //go:build expression mentions <tag>, negated or
#                          not, one `./path` per line, REPO-ROOT-relative (a
#                          submodule caller passes a pathspec and rewrites the
#                          prefix). Empty output is a valid answer and the caller
#                          must handle it: aiming
#                          golangci-lint at an empty package list fails with
#                          `no go files to analyze`, exit 5, which reads as a
#                          lint failure rather than "nothing to do".
#
# Tracked .go files only, `_test.go` included. The expression is tokenized, never
# substring-matched: `myrace` and `race_detector` are not `race`.
set -uo pipefail

# One entry per tag, each naming the pass that reads it. Negations are implied:
# `!x` is covered by whichever pass reads the UNtagged side.
#   integration — golangci `run.build-tags: [integration]` (make lint, both CI
#                 lint jobs) and gosec invocation 1 (`-tags integration`).
#   race        — the race-scoped golangci pass and gosec invocation 2
#                 (`-tags integration,race`), both over --packages-for race.
#   windows     — CI's `GOOS: windows` golangci pass and gosec invocation 3
#                 (`GOOS=windows`). The `!windows` side is the default build.
ALLOWED="integration race windows"

cd "$(git rev-parse --show-toplevel)" || exit 1

# tags_of <expression> — the identifiers in one //go:build expression, one per
# line. Operators and parentheses are separators; `!` binds to its term and a
# negated tag is the same tag for both callers.
tags_of() {
  printf '%s\n' "${1#*//go:build}" \
    | tr '()!' '   ' \
    | sed 's/&&/ /g; s/||/ /g' \
    | tr -s '[:space:]' '\n' \
    | grep -v '^$' || true
}

if [ "${1:-}" = "--packages-for" ]; then
  want="${2:?--packages-for needs a tag}"
  shift 2
  # git ORs multiple pathspecs, so a caller's scope cannot be ANDed with a `*.go`
  # pathspec — the .go filter is applied per line instead.
  scope=("$@"); [ ${#scope[@]} -eq 0 ] && scope=(.)
  while IFS=: read -r file _line expr; do
    case "$file" in *.go) ;; *) continue ;; esac
    for tag in $(tags_of "$expr"); do
      [ "$tag" = "$want" ] && printf './%s\n' "$(dirname "$file")"
    done
  done < <(git grep -n '^//go:build' -- "${scope[@]}") | sort -u
  exit 0
fi

violations=""
while IFS=: read -r file _line expr; do
  for tag in $(tags_of "$expr"); do
    case " $ALLOWED " in
      *" $tag "*) ;;
      *) violations="${violations}${file}: ${tag}"$'\n' ;;
    esac
  done
done < <(git grep -n '^//go:build' -- '*.go')

if [ -z "$violations" ]; then
  exit 0
fi

printf '%s' "$violations" | sort -u
cat >&2 <<'MSG'

check-build-tags: a build tag appeared that no lint or security pass reads.
A file behind it is invisible to every gate, and a gate that reads nothing
still exits 0. Do one of:
  - add a pass that reads the new tag's side (see wiki/linting.md), or
  - add the tag to ALLOWED in this script with a comment naming that pass.
MSG
exit 1
