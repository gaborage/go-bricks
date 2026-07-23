#!/usr/bin/env bash
# Cut a signed release tag for the go-bricks-migrate CLI (tools/migration).
# See RELEASING.md §6. Usage: make release-cli VERSION=v0.53.0
# Run AFTER the framework tag vX.Y.Z is on the module proxy AND
# tools/migration/go.mod pins go-bricks vX.Y.Z on main.
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }

ROOT="$(git rev-parse --show-toplevel)" || die "not in a git repo"
cd "$ROOT"

VERSION="${VERSION:-}"
[ -n "$VERSION" ] || die "VERSION is required, e.g. VERSION=v0.53.0"
TAG="tools/migration/$VERSION"

# 1. strict pre-1.0 semver (widen at v1.0)
[[ "$VERSION" =~ ^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] \
  || die "VERSION '$VERSION' is not strict v0.MINOR.PATCH (pre-1.0 only)"

# 2. reconciliation: the CLI has no release-please manifest — its version source
#    of truth is the go-bricks version pinned in tools/migration/go.mod (the CLI
#    is released at the same number as the framework it links).
PINNED="$(grep -oE 'gaborage/go-bricks v[0-9]+\.[0-9]+\.[0-9]+' tools/migration/go.mod | grep -oE 'v[0-9.]+' | head -1 || true)"
[ "$PINNED" = "$VERSION" ] \
  || die "VERSION ($VERSION) != go-bricks pinned in tools/migration/go.mod (${PINNED:-none}). Cut the CLI tag at the version its go.mod pins."

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

# 6. the framework tag must be resolvable on the module proxy (RELEASING.md §6 precondition)
GOWORK=off go list -m "github.com/gaborage/go-bricks@$VERSION" >/dev/null 2>&1 \
  || die "go-bricks $VERSION is not resolvable on the module proxy yet; wait and retry"

# 7. the pin must be tidy — no redundant commit needed (Renovate/PR already landed it)
GOWORK=off go -C tools/migration mod tidy
[ -z "$(git status --porcelain tools/migration/go.mod tools/migration/go.sum)" ] \
  || { git checkout -- tools/migration/go.mod tools/migration/go.sum; die "go mod tidy rewrote tools/migration/go.mod/go.sum — the pin on main is not tidy; fix via a normal PR first"; }

# 8. CLI tag absent locally + on origin + strictly greater than the latest CLI tag
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null 2>&1 && die "tag $TAG already exists locally"
git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1 && die "tag $TAG already exists on origin"
LATEST_CLI="$(git tag -l 'tools/migration/v0.*' --sort=-v:refname | head -n1)"
if [ -n "$LATEST_CLI" ]; then
  LATEST_VER="${LATEST_CLI#tools/migration/}"
  { [ "$VERSION" != "$LATEST_VER" ] && [ "$(printf '%s\n%s\n' "$LATEST_VER" "$VERSION" | sort -V | tail -n1)" = "$VERSION" ]; } \
    || die "VERSION ($VERSION) must be strictly greater than latest CLI tag ($LATEST_VER)"
fi

# 9. CI status for THIS commit — ADVISORY only. The migrate-tool jobs are
#    path-skipped when HEAD didn't touch tools/migration, and the local gate
#    (step 10) re-verifies the CLI against the released framework regardless.
CI_STATUS="$(gh run list --workflow ci-v2.yml --branch main --commit "$LOCAL_SHA" --limit 20 \
  --json headSha,status,conclusion \
  --jq '[.[] | select(.headSha=="'"$LOCAL_SHA"'")] | (map(select(.status=="completed")) | first) // .[0] | if . == null then "absent" else "\(.status):\(.conclusion // "none")" end' 2>/dev/null || echo "absent")"
case "$CI_STATUS" in
  completed:success)
    echo "CI green for $LOCAL_SHA" ;;
  completed:failure|completed:cancelled|completed:timed_out|completed:startup_failure|completed:action_required)
    die "CI for $LOCAL_SHA is '$CI_STATUS' — fix before releasing" ;;
  *)
    echo "WARN: CI status '$CI_STATUS' for $LOCAL_SHA — proceeding (the local gate below re-verifies the CLI against the released framework)" ;;
esac

# 10. full local gate against the RELEASED framework. GOWORK=off is MANDATORY —
#     go.work would otherwise build the CLI against local framework source.
GOWORK=off make -C tools/migration check
GOWORK=off make -C tools/migration sec
# a fmt inside 'check' that rewrote tracked files means the commit isn't fmt-clean,
# yet the tag would point at the un-rewritten commit. Fail loudly.
[ -z "$(git status --porcelain)" ] \
  || { git checkout -- .; die "the CLI gate (fmt) modified tracked files; the commit is not fmt-clean. Fix on main first."; }

# 11. signing probe BEFORE mutating refs (cleans up even if interrupted)
PROBE="_release-cli-sign-probe-$$"
trap 'git tag -d "$PROBE" >/dev/null 2>&1 || true' EXIT INT TERM
git tag -s -m "signing probe" "$PROBE" HEAD \
  || die "signing failed — unlock 1Password / start the SSH agent and retry. NEVER use --no-sign."
git tag -d "$PROBE" >/dev/null 2>&1 || true
trap - EXIT

# 12. signed annotated tag. -s is MANDATORY.
git tag -s "$TAG" -m "go-bricks-migrate $VERSION"

# 13. BLOCKING local signature verify against the REPO allowlist — the same trust
#     root CI uses (.github/allowed_signers), NOT personal git config.
git -c gpg.format=ssh -c gpg.ssh.allowedSignersFile=.github/allowed_signers tag -v "$TAG" \
  || { git tag -d "$TAG"; die "tag signature failed local verify against .github/allowed_signers (key/principal mismatch?) — tag deleted"; }

# 14. push the tag. Fires migrate-cli-tag.yml (Part B), which closes the drift issue once fresh.
git push origin "$TAG" || { git tag -d "$TAG"; die "tag push failed; local tag removed — re-run: make release-cli VERSION=$VERSION"; }
echo "Pushed $TAG. If a CLI-tag drift issue was open, migrate-cli-tag.yml will close it once it confirms freshness."
