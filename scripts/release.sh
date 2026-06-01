#!/usr/bin/env bash
# Cut a signed release tag for go-bricks. See RELEASING.md.
# Usage: make release VERSION=v0.38.0   (run AFTER merging the release-please PR)
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }

ROOT="$(git rev-parse --show-toplevel)" || die "not in a git repo"
cd "$ROOT"

VERSION="${VERSION:-}"
[ -n "$VERSION" ] || die "VERSION is required, e.g. VERSION=v0.38.0"

# 1. strict pre-1.0 semver (widen at v1.0)
[[ "$VERSION" =~ ^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] \
  || die "VERSION '$VERSION' is not strict v0.MINOR.PATCH (pre-1.0 only)"

# 2. version reconciliation: VERSION == manifest == CHANGELOG top section
[ -f .release-please-manifest.json ] || die "missing .release-please-manifest.json"
MANIFEST_VER="v$(jq -r '."."' .release-please-manifest.json)"
[ "$VERSION" = "$MANIFEST_VER" ] \
  || die "VERSION ($VERSION) != release-please manifest ($MANIFEST_VER). Merge the 'chore(main): release' PR first."
[ -f CHANGELOG.md ] || die "missing CHANGELOG.md"
CHANGELOG_VER="$(grep -m1 -oE '^## \[v?[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ "v${CHANGELOG_VER:-}" = "$VERSION" ] \
  || die "CHANGELOG top section (v${CHANGELOG_VER:-none}) != VERSION ($VERSION) — did you merge the release PR?"

# 3. on main, clean tree
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "not on main"
[ -z "$(git status --porcelain)" ] || die "working tree is dirty"

# 4. gh authenticated AND can read this repo (keyring flips work/personal)
gh auth status >/dev/null 2>&1 || die "gh not authenticated; run: gh auth login"
gh repo view gaborage/go-bricks --json name >/dev/null 2>&1 \
  || die "active gh account cannot read gaborage/go-bricks; run: gh auth switch -u gaborage"

# 5. in sync with origin/main (fetch FIRST so the compare isn't stale)
git fetch --quiet --prune --tags origin
LOCAL_SHA="$(git rev-parse HEAD)"
[ "$LOCAL_SHA" = "$(git rev-parse origin/main)" ] || die "local main not in sync with origin/main; git pull"

# 6. tag absent + strictly greater than latest
git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null 2>&1 && die "tag $VERSION already exists locally"
git ls-remote --exit-code --tags origin "refs/tags/$VERSION" >/dev/null 2>&1 && die "tag $VERSION already exists on origin"
LATEST="$(git tag -l 'v0.*' --sort=-v:refname | head -n1)"
if [ -n "$LATEST" ]; then
  { [ "$VERSION" != "$LATEST" ] && [ "$(printf '%s\n%s\n' "$LATEST" "$VERSION" | sort -V | tail -n1)" = "$VERSION" ]; } \
    || die "VERSION ($VERSION) must be strictly greater than latest tag ($LATEST)"
fi

# 7. CI green for THIS exact commit; accept path-filtered skip for release-please commits
CI_STATUS="$(gh run list --workflow ci-v2.yml --branch main --commit "$LOCAL_SHA" --limit 20 \
  --json headSha,status,conclusion \
  --jq '[.[] | select(.headSha=="'"$LOCAL_SHA"'")] | (map(select(.status=="completed")) | first) // .[0] | "\(.status):\(.conclusion // "none")"' 2>/dev/null || echo "")"
case "$CI_STATUS" in
  completed:success) echo "CI green for $LOCAL_SHA" ;;
  "") echo "WARN: no CI run for $LOCAL_SHA (expected for a CHANGELOG/manifest-only release-please commit; release.yml re-verifies the tagged commit)" ;;
  completed:*) die "CI for $LOCAL_SHA is '$CI_STATUS' (not success) — fix before releasing" ;;
  *) die "CI for $LOCAL_SHA is still '$CI_STATUS' — wait for it to finish" ;;
esac

# 8. full local gate, BOTH modules (same commands CI runs via the Makefile targets)
make check
make vuln
make sec
( cd tools/migration && go build ./... && go test ./... )
make -C tools/migration vuln
make -C tools/migration sec

# 9. signing probe BEFORE mutating refs (cleans up even if interrupted)
PROBE="_release-sign-probe-$$"
trap 'git tag -d "$PROBE" >/dev/null 2>&1 || true' EXIT
git tag -s -m "signing probe" "$PROBE" HEAD \
  || die "signing failed — unlock 1Password / start the SSH agent and retry. NEVER use --no-sign."
git tag -d "$PROBE" >/dev/null 2>&1
trap - EXIT

# 10. signed annotated tag on HEAD (the merged release-please commit). -s is MANDATORY.
git tag -s "$VERSION" -m "Release $VERSION"

# 11. BLOCKING local signature verify when an allowlist is configured
if [ -n "$(git config --get gpg.ssh.allowedSignersFile || true)" ]; then
  git tag -v "$VERSION" \
    || { git tag -d "$VERSION"; die "tag signature failed local verify (key/principal mismatch?) — tag deleted"; }
fi

# 12. push the tag (fires release.yml). main is already at HEAD (release-please PR merge).
git push origin "$VERSION" || die "tag push failed; retry: git push origin $VERSION"
echo "Pushed $VERSION. release.yml will re-verify + publish. Watch: gh run watch"
