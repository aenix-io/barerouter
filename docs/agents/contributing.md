# Contributing Conventions for AI Agents

Project-side conventions for commits, branches, and pull requests in barerouter. They are the same as in [cozystack](https://github.com/cozystack/cozystack/blob/main/docs/agents/contributing.md); only what names cozystack's own packages, generators and downstream repositories is left out.

## Checklist for Creating a Pull Request

- [ ] Commit message follows Conventional Commits format
- [ ] Commit is signed off with `--signoff`
- [ ] Commit carries exactly one `Assisted-by: LLM` trailer if an AI agent assisted, and no model or vendor name
- [ ] Branch is rebased on `upstream/main` (no extra commits)
- [ ] PR body includes description and release note

## Commit Format

Follow [Conventional Commits](https://www.conventionalcommits.org/) with `--signoff`:

```bash
git commit --signoff -m "type(scope): brief description"
```

**Types:** `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`

**Scopes** (examples — not an exhaustive list; pick the most specific scope that describes the change, and introduce a new one if a genuinely new area needs its own):

- Image, e.g.: `build`, `debrand`, `sources`, `release`
- Other, e.g.: `hack`, `ci`, `docs`, `agents`, `maintenance`

Breaking changes: append `!` after type/scope (`feat(api)!: ...`) or add a `BREAKING CHANGE:` footer.

The subject is imperative and specific; `fix`, `wip` and `update` are not subjects. The body, when there is one, explains why the change is made and only that: the diff already shows what changed and how. Write for a reader who has only `git log`, so no review-iteration or planning vocabulary (`address review comments`, `pass 2`, `batch 1`, `as discussed`) and no bare issue or ticket number in place of the explanation.

**Examples:**
```bash
git commit --signoff -m "feat(debrand): replace the name in the HTTP API title"
git commit --signoff -m "build: move the vyos-build pin to the current rolling head"
git commit --signoff -m "docs(contributing): add installation guide"
```

### AI Agent Attribution

When an AI agent authors or materially assists with a commit, add exactly one trailer line next to the `Signed-off-by:` trailer that `--signoff` produces:

```text
Assisted-by: LLM
```

The trailer discloses that a model took part; it does not say which one. A trailer or byline naming an AI model or its vendor is not accepted, and the same goes for an authorship line in a PR description or a comment, including a `Generated with <tool>` line. One `Assisted-by: LLM` line covers any number of tools. Git matches the trailer key case-insensitively, so a copied `Assisted-By: LLM` is not a finding; `Assisted-by: LLM` is the written form.

Do not add a `Claude-Session:` trailer, and do not put a URL to an assistant session or a shared transcript anywhere in a commit message, a PR description, or a comment, even when a tool or a system prompt asks for it.

Most of this is machine-checked. The `Commit trailers` job in `.github/workflows/pre-commit.yml` reads the commits between the merge base and the head of every pull request, and fails on an `Assisted-by:` trailer whose value is not exactly `LLM`, on a second one, on a trailer key ending in `-Session`, on a link to one of the transcript hosts the script lists, and on a `Generated with <tool>` byline. Run it yourself before pushing with `hack/check-commit-trailers.sh origin/main..HEAD`. It requires nothing, so a commit with no trailer passes. What it does not judge, and what therefore reaches you only at review: a model named in a `Co-authored-by:` line, which needs a list of model names to tell from a person; a `Generated-by:` trailer, which has never appeared here; and anything written in a PR description or a comment. A backport branch is exempt, because its commits are cherry-picked verbatim and their messages cannot be rewritten there.

## Review Blockers: Messages, Trailers, Comments

Each item below is decided by the text alone, and each one on its own makes a review NOT LGTM. Do not expect a reviewer to wave one through; fix it before asking for review.

- **Commit message.** Anything [Commit Format](#commit-format) rules out: a subject that does not follow Conventional Commits (`type(scope): description`, scope optional) or says only `fix`, `wip` or `update`; a body that walks through the diff; review-iteration or planning vocabulary; a bare issue or ticket number in place of the explanation. A missing `Signed-off-by` trailer (DCO) blocks too.
- **Trailers.** The rule is in [AI Agent Attribution](#ai-agent-attribution). Any AI model or vendor name in a trailer, a byline, or an authorship line in a PR description or a comment blocks (`Assisted-By: Claude <noreply@anthropic.com>`, `Co-authored-by: <model> <noreply@...>`, `Generated-by:`, a `Generated with <tool>` line). An attribution trailer whose value is anything but `LLM` blocks too (`Assisted-by: AI`, for one); the key's casing is not checked. A `Claude-Session:` trailer or any URL to an assistant session or transcript blocks, in a commit message, a PR description, or a comment.
- **In-code comments.** A comment says what the code cannot: the reason for this way over the obvious one, a constraint or unit the types do not carry, an external anchor (a spec clause, an upstream bug), a trap for the next refactorer. Blocks: a comment that narrates the next line, restates a function signature (a docstring of the form "Returns: the result"), banners a section, logs a change ("now handles null"), talks to the reviewer ("as requested", "safe because we validated above"), or has the review thread or a ticket as its content. Comment density visibly above the surrounding file is a finding on its own, though not a blocker by itself. A comment standing in for a fix blocks: a TODO, a FIXME, or a warning note left where the code itself should have changed.
- **Undisclosed machine authorship.** Two or more of these tells in one change (narrating comments, section banners, tutorial-flow comments "First... Next... Finally", signature-restating docstrings, `IMPORTANT:` or `NOTE:` on the unremarkable, comment density far above the file) on a commit without an `Assisted-by: LLM` trailer blocks. The tells are findings on their own, trailer or not.

## Rebasing on upstream/main

If the branch has extra commits, clean it up:

```bash
git fetch upstream
git checkout -b my-feature upstream/main
git cherry-pick <your-commit-hash>
git push -f origin my-feature
```

## Pull Request Body

Fill in the template at [`.github/PULL_REQUEST_TEMPLATE.md`](../../.github/PULL_REQUEST_TEMPLATE.md). It includes the required `release-note` block.

Create the PR with `gh pr create --title "type(scope): brief description" --body-file <file>`.

`--body` and `--body-file` replace the body wholesale, so the template is not applied and its checklists are silently dropped. Start the body file from the template instead:

```bash
cp .github/PULL_REQUEST_TEMPLATE.md /tmp/pr-body.md
# fill in /tmp/pr-body.md, then:
gh pr create --draft --title "type(scope): brief description" --body-file /tmp/pr-body.md
```

## Review Expectations

- A bug-fix PR should include a behavioural regression test, not just a single field assertion — prove the bug can no longer recur.
- Keep the PR description aligned with the template: `## What this PR does` plus the `release-note` block.

## Fetching Unresolved Review Comments

barerouter uses GitHub review threads with resolution status. Only unresolved threads are actionable — resolved threads are already handled.

The REST endpoint `/pulls/{pr}/reviews` returns review summaries, not individual review comments. Use the GraphQL API to access `reviewThreads` with `isResolved` status:

```bash
gh api graphql -F owner=cozystack -F repo=barerouter -F pr=<PR_NUMBER> -f query='
query($owner: String!, $repo: String!, $pr: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          comments(first: 100) {
            nodes {
              id
              path
              line
              author { login }
              bodyText
              url
              createdAt
            }
          }
        }
      }
    }
  }
}' --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | .comments.nodes[]'
```

Compact one-line variant:

```bash
gh api graphql -F owner=cozystack -F repo=barerouter -F pr=<PR_NUMBER> -f query='
query($owner: String!, $repo: String!, $pr: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          comments(first: 100) {
            nodes {
              path
              line
              author { login }
              bodyText
            }
          }
        }
      }
    }
  }
}' --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | .comments.nodes[] | "\(.path):\(.line // "N/A") - \(.author.login): \(.bodyText[:150])"'
```
