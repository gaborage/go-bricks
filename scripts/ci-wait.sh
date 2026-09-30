#!/usr/bin/env bash
# Wait for the ci-v2 run of a pushed commit and exit with its result.
#
#   scripts/ci-wait.sh <full 40-hex sha>
#
# Run it as ONE background command (the CLAUDE.md CI-wait rule). It watches the
# commit's newest ci-v2 run at a 30s interval, retrying every 30s while none is
# listed yet (right after a push). It never falls back to an older run. ci-v2
# keeps one live run per PR: when the watched run is cancelled, a newer run of
# the same commit (a stack-push twin) is its replacement and is watched next.
#
# Exit: 0 success, 1 failure, 2 usage, 3 the commit's newest run was cancelled
# (a newer head superseded it, or it was cancelled by hand).
set -euo pipefail

sha="${1:-}"
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 <full 40-hex sha>" >&2; exit 2; }

for _ in $(seq 20); do
  if ! runs=$(gh run list --commit "$sha" --workflow ci-v2.yml --json databaseId,conclusion,headSha,createdAt); then
    echo "ci-wait: listing ci-v2 runs for $sha failed; retrying" >&2
    sleep 30
    continue
  fi
  id="" conclusion=""
  read -r id conclusion < <(jq -r --arg s "$sha" \
    '[.[] | select(.headSha == $s)] | sort_by(.createdAt, .databaseId) | last // empty | "\(.databaseId) \(.conclusion)"' \
    <<<"$runs") || true
  if [ -z "$id" ]; then
    sleep 30
    continue
  fi
  if [ "$conclusion" = "cancelled" ]; then
    echo "ci-wait: run $id, the newest for $sha, was cancelled (a newer head superseded it, or it was cancelled by hand)" >&2
    exit 3
  fi
  if gh run watch "$id" --interval 30 --exit-status; then
    exit 0
  fi
  conclusion=$(gh run view "$id" --json conclusion -q .conclusion) || conclusion=""
  case "$conclusion" in
    "")
      echo "ci-wait: run $id has no conclusion yet (watch interrupted); retrying" >&2
      sleep 30
      ;;
    cancelled)
      echo "ci-wait: run $id was cancelled; checking for a newer run of the same commit" >&2
      ;;
    *)
      exit 1
      ;;
  esac
done
echo "ci-wait: no finished ci-v2 run for $sha" >&2
exit 1
