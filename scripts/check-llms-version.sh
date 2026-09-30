#!/usr/bin/env bash
# Release-PR gate (#1565): llms.txt must declare the version the release-please
# manifest is about to release, and carry a ladder clause for it.
#
#   usage: scripts/check-llms-version.sh [manifest] [llms]
#          (defaults: .release-please-manifest.json llms.txt)
#
# Both rules read ONE line: the `- Reflects **go-bricks vX.Y.Z**` bullet inside
# the `## Version & Compatibility` section.
#   declaration  the bold `**go-bricks vX.Y.Z**` names exactly the manifest's
#                version.
#   ladder       the rest of that bullet names the same version followed by a
#                space and a lowercase word (`v0.70.0 made …`), the shape of every
#                narrative clause. The declaration itself does not count.
#
# ci-v2.yml's llms-version job runs this on the release-please PR only; see
# RELEASING.md §1 for the docs(llms) PR that has to merge first. Exits 0 when
# both rules hold, 1 with a `::error` line per broken rule otherwise.
set -euo pipefail

manifest="${1:-.release-please-manifest.json}"
llms="${2:-llms.txt}"

fail() { echo "::error file=$llms::$1"; status=1; }
status=0

version="$(jq -er '."."' "$manifest")"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "::error file=$manifest::manifest '.' version is not X.Y.Z"
  exit 1
fi
tag="v$version"
tag_re="${tag//./\\.}"

bullet="$(awk '
  /^## / { in_section = ($0 == "## Version & Compatibility") ; next }
  in_section && /^- Reflects \*\*go-bricks v/ { print; exit }
' "$llms")"
if [ -z "$bullet" ]; then
  echo "::error file=$llms::no '- Reflects **go-bricks vX.Y.Z**' bullet under '## Version & Compatibility'"
  exit 1
fi

declared="$(printf '%s\n' "$bullet" | sed -nE 's/^- Reflects \*\*go-bricks (v[0-9]+\.[0-9]+\.[0-9]+)\*\*.*/\1/p')"
if [ "$declared" != "$tag" ]; then
  fail "llms.txt declares go-bricks ${declared:-<unparseable>}, the release-please manifest releases $tag: bump the Version & Compatibility bullet"
fi

rest="${bullet#*\*\*go-bricks "$declared"\*\*}"
if ! printf '%s\n' "$rest" | grep -qE "(^|[^0-9A-Za-z.])${tag_re} [a-z]"; then
  fail "llms.txt has no ladder clause for $tag: add '$tag <verb> …' to the Version & Compatibility bullet, summarizing that hop in wiki/migrations.md"
fi

if [ "$status" -eq 0 ]; then
  echo "llms.txt declares $tag and carries its ladder clause."
fi
exit "$status"
