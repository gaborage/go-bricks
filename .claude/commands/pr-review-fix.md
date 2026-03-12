# PR Review Fix

Fix all review comments on a pull request in a single pass.

## Usage
Provide a PR number or URL as argument. If omitted, uses the current branch's PR.

## Workflow

1. **Gather context**: Run `gh pr view` to get PR details and `gh api repos/{owner}/{repo}/pulls/{number}/comments` to fetch all review comments.
2. **Analyze**: Read ALL review comments before making any changes. Group them by file and identify dependencies between comments.
3. **Implement**: Fix all comments in one pass. For each comment:
   - Read the referenced file and surrounding context
   - Apply the requested change
   - If a comment is unclear, use your best judgment based on the codebase patterns
4. **Verify**: Run `make check` to ensure all fixes pass fmt, lint, and tests.
5. **Commit and push**: Create a single commit with a message summarizing all fixes, then push.

## Rules
- NEVER push incremental fixes. All comments must be addressed in a single commit.
- If `make check` fails after fixes, fix the issues and re-run until it passes.
- If a review comment conflicts with another, flag it to the user before proceeding.
- Respect existing code patterns and conventions in the codebase.
