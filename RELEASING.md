# Releasing GoBricks

GoBricks is released with an **automated-calculation, scripted-signed-publish** flow.
`release-please` computes the version and writes the changelog; **you** merge its
Release PR and cut a **signed** tag locally; CI re-verifies and publishes.

- **Version source of truth:** `.release-please-manifest.json` (bot-maintained). The
  **git tag** is the release boundary you sign. There is no hand-maintained VERSION.
- **Two modules, one framework tag stream:** only the root module is release-please-managed
  and tagged `vX.Y.Z`. The `go-bricks-migrate` CLI is tagged manually `tools/migration/vX.Y.Z`
  (see §6).
- **Pre-1.0 SemVer:** `feat` → MINOR, `fix` → PATCH, breaking is **capped to MINOR** while
  `0.x` (project convention; SemVer §4 permits anything pre-1.0). Mark breaking changes with
  `feat!:` / `BREAKING CHANGE:` so the bump + the ⚠ banner are correct.

## 0. One-time setup

- **`RELEASE_PLEASE_TOKEN`** — a fine-grained PAT (repo `gaborage/go-bricks`; permissions
  **Contents: Read and write** + **Pull requests: Read and write**), stored as a repo secret.
  release-please uses it so its Release PR triggers CI and its label edits are reliable — the
  default `GITHUB_TOKEN` does neither. Create it once:
  `gh secret set RELEASE_PLEASE_TOKEN --repo gaborage/go-bricks`.
- **Squash setting** — Settings → General → Pull Requests → **"Default to PR title for squash
  merge commits"** must be ON (release-please parses the PR title as the commit subject).
- **Tag/branch protection** — `.github/allowed_signers` is the CI trust root for release-tag
  signatures. `release.yml` verifies a tag's signature against the copy of `allowed_signers`
  **on `main`** and requires the tagged commit to be an ancestor of `main`, so the trust root is
  exactly what branch protection guards. For defense-in-depth, add a **tag-protection ruleset**
  on `v*` restricting tag creation to the maintainer.

## 1. release-please keeps a standing Release PR

On every push to `main`, `release-please` opens/updates a `chore(main): release vX.Y.Z` PR
that computes the next version from Conventional-Commit PR titles and writes the
`CHANGELOG.md` section + bumps `.release-please-manifest.json`. It does **not** tag or publish.

## 2. Cut the release (local)

```bash
# 1. Merge the standing "chore(main): release vX.Y.Z" PR. This lands CHANGELOG + manifest on main.
#    (If the breaking section needs ADR / migration links, add them to CHANGELOG.md in a quick
#     follow-up commit on main BEFORE the next step.)
git checkout main && git pull
# 2. Immediately cut the signed tag (read the version from the merged PR / manifest):
make release VERSION=v0.38.0
```

`make release` is **read-and-verify-only**: it asserts `VERSION == .release-please-manifest.json
== CHANGELOG top section`, runs the full gate — `make check` + `make vuln` + `make sec` for the
root module, and build + test + `make vuln` + `make sec` for the `tools/migration` module — probes
signing, creates a **signed annotated** tag (`git tag -s`), verifies the signature locally, and
pushes the tag. It does **not** edit `CHANGELOG.md` (release-please owns it).

> **Rule (release-please #1561):** never merge a Release PR you are not ready to `make release`
> immediately. A merged-but-untagged Release PR keeps its `autorelease: pending` label and
> **deadlocks all future Release PRs**. `release.yml` clears that label after publishing.

## 3. What `release.yml` does (on tag push)

Re-verifies the **tagged commit** independently — **framework**: build + `go test -race ./...` +
`go mod tidy` + `make vuln`/`make sec`; **CLI**: build + `validate-cli` + `make vuln`/`make sec`
— asserts the tag is annotated + SSH-signature-valid against `.github/allowed_signers` on `main`,
publishes the GitHub Release using the `CHANGELOG.md` section as the body, then clears the merged
Release PR's `autorelease: pending` label. CI never signs.

## 4. If the release fails

- **Before the tag is pushed:** fix locally, re-run `make release`.
- **`release.yml` red, tag already pushed:** do NOT reuse the version. Yank the tag (§5), fix
  forward, bump PATCH, re-release.

## 5. Yanking / retracting a bad version

```bash
git tag -d v0.38.0
git push origin :refs/tags/v0.38.0
gh release delete v0.38.0 --yes   # if a release was created
```
A tag delete does NOT recall a version already pulled via `go get`. Ship a follow-up release
adding a `retract` directive to `go.mod`:
```go
retract v0.38.0 // <reason>; use v0.38.1+
```

## 6. The go-bricks-migrate CLI

Released manually at the same number as the framework, **after** the framework tag is on the
module proxy:
```bash
go list -m github.com/gaborage/go-bricks@v0.38.0          # wait until this resolves
GOWORK=off go -C tools/migration get github.com/gaborage/go-bricks@v0.38.0
GOWORK=off go -C tools/migration mod tidy
git commit -S -am "chore(migrate): pin go-bricks v0.38.0"
git tag -s tools/migration/v0.38.0 -m "go-bricks-migrate v0.38.0"
git push origin tools/migration/v0.38.0
```

## 7. Security properties NOT yet provided (Phase 2)

This flow provides **tag-signature provenance only** — verified against a committed
`.github/allowed_signers` allowlist (protected by branch protection), **not** a CA. It does
**NOT** provide artifact signing (cosign), SBOM, or SLSA build provenance, and ships no
compiled binaries. **Do not represent GoBricks releases as supply-chain-attested.** Deferred:
goreleaser binaries, Homebrew, cosign, SBOM, SLSA.
