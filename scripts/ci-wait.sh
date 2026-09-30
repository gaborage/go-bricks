#!/usr/bin/env bash
# Wait for the ci-v2 run of a pushed commit and exit with its result.
#
#   scripts/ci-wait.sh <full 40-hex sha>
#
# Run it as ONE background command (the CLAUDE.md CI-wait rule). It lists the
# commit's non-cancelled ci-v2 run, retrying every 30s while none is listed yet
# (right after a push), and watches it at a 30s interval. ci-v2 keeps one live
# run per PR: when the watched run ends cancelled, the script follows a
# replacement run of the same commit (a stack-push twin) if there is one.
#
# Exit: 0 success, 1 failure, 2 usage, 3 cancelled with no replacement run for
# this commit (a newer head superseded it, or it was cancelled by hand).
set -euo pipefail

sha="${1:-}"
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 <full 40-hex sha>" >&2; exit 2; }

cancelled=""
for _ in $(seq 20); do
  id=$(gh run list --commit "$sha" --workflow ci-v2.yml --json databaseId,conclusion,headSha \
    | jq -r --arg s "$sha" '[.[] | select(.headSha == $s and .conclusion != "cancelled")][0].databaseId // empty') \
    || { echo "ci-wait: listing ci-v2 runs for $sha failed; retrying" >&2; id=""; }
  if [ -z "$id" ]; then
    if [ -n "$cancelled" ]; then
      echo "ci-wait: run $cancelled was cancelled and $sha has no replacement run (a newer head superseded it, or it was cancelled by hand)" >&2
      exit 3
    fi
    sleep 30
    continue
  fi
  if gh run watch "$id" --interval 30 --exit-status; then
    exit 0
  fi
  conclusion=$(gh run view "$id" --json conclusion -q .conclusion) || conclusion=""
  if [ -z "$conclusion" ]; then
    echo "ci-wait: run $id has no conclusion yet (watch interrupted); retrying" >&2
    sleep 30
    continue
  fi
  if [ "$conclusion" != "cancelled" ]; then
    exit 1
  fi
  cancelled="$id"
  echo "ci-wait: run $id was cancelled; looking for a replacement run of the same commit" >&2
done
echo "ci-wait: no finished ci-v2 run for $sha" >&2
exit 1
