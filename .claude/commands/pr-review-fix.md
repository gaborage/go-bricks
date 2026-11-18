# PR Review Fix

Fix all review findings on a pull request in a single pass.

## Usage

Provide a PR number or URL as argument. If omitted, uses the current branch's PR.

## Workflow

1. **Gather context**: Run `gh pr view` with the PR number or URL given as argument (with no argument it selects the current branch's PR) to get PR details, including the PR number. With that number in place of `NUMBER` (`gh api` fills `{owner}` and `{repo}` itself, and nothing else), run `gh api --paginate repos/{owner}/{repo}/pulls/NUMBER/comments` to fetch all inline review comments, `gh api --paginate repos/{owner}/{repo}/pulls/NUMBER/reviews` to fetch the review summaries, and `gh api --paginate repos/{owner}/{repo}/issues/NUMBER/comments` to fetch the PR conversation comments, where a reviewer can also post a finding. Then collect the findings of every automated reviewer, as root `CLAUDE.md` PR Review Workflow requires, following its SonarCloud procedure.
2. **Analyze**: Read every finding before making any changes. Group them by file and identify dependencies between findings.
3. **Implement**: Fix all findings in one pass. For each finding:
   - Read the referenced file and surrounding context
   - Apply the requested change, or document the skip and its reason in the commit message
   - If a finding is unclear, use your best judgment based on the codebase patterns
4. **Verify**: Run `make check`, then the pre-push gates and `make mutate` exactly as root `CLAUDE.md` Workflow Rules define them.
5. **Commit and push**: Create a single commit with a message summarizing all fixes and any documented skips, then push once, to the destination the push rule below names.

## Rules

- If the PR URL names a repository other than the current checkout, stop before collecting findings and tell the user: `gh api` fills `{owner}` and `{repo}` from the checkout, and the edit and push steps apply to the checked-out branch.
- Before the first edit, put the checkout on the PR head, with `NUMBER` as in step 1. Stop and tell the user when `gh pr view NUMBER --json isCrossRepository` reports a fork PR (the push step targets this repository), or when `git status --porcelain --untracked-files=no` lists any staged or unstaged change. Otherwise run `gh pr checkout NUMBER`, even when the branch name already matches: it switches to the PR's head branch and fast-forwards it to the PR head. If it fails, as it does when the local branch has diverged, stop and tell the user.
- Push with an explicit destination, never a bare `git push`: `gh pr checkout` leaves the upstream of an existing local branch as it was, and that upstream can point at another repository. Run `git push REMOTE HEAD:refs/heads/BRANCH`, where `BRANCH` is the name `gh pr view NUMBER --json headRefName` reports and `REMOTE` is the remote whose URL in `git remote -v` names the repository `gh repo view --json nameWithOwner` reports. If no remote has that URL, stop and tell the user.
- If `make check` or a pre-push gate fails after fixes, fix the issues and re-run until it passes.
- If a finding conflicts with another, flag it to the user before proceeding.
- Respect existing code patterns and conventions in the codebase.
