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
5. **Commit and push**: Create a single commit with a message summarizing all fixes and any documented skips, then push once.

## Rules

- If `make check` or a pre-push gate fails after fixes, fix the issues and re-run until it passes.
- If a finding conflicts with another, flag it to the user before proceeding.
- Respect existing code patterns and conventions in the codebase.
