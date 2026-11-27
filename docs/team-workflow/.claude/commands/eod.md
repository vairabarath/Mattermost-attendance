---
description: Draft my end-of-day report from today's git commits (summary only — nothing is changed or sent)
allowed-tools: Bash(git config:*), Bash(git log:*), Bash(git branch:*), Bash(git symbolic-ref:*), Bash(git rev-parse:*), Bash(date:*)
---

Draft my end-of-day report for the attendance bot from **today's git commits in this repository**. This is a summary only.

**Rules — follow them strictly:**

- Only **read** the repository with the commands below. Do **not** commit, push, fetch, switch branches, stash, or change any file.
- Do **not** call any API or network service (no `gh`, no `curl`, no GitHub or Mattermost) — git history only.
- Don't invent work: describe only what the commits show.

**Steps:**

1. Find me and today: `git config user.email` (if empty, say so and stop) and `date +%Y-%m-%d`.
2. Today's commits by me on all branches, with the branch each came from:
   `git log --all --since="<today> 00:00" --author="<email>" --no-merges --source --format='%h%x09%S%x09%ad%x09%s' --date=format:%H:%M`
   (`%S` is the ref the commit was reached from.) If there are none, say "No commits today in this repository" and stop.
3. Group the commits by **issue number**: from the branch name (`42-session-timeout` → #42; `refs/heads/` or `refs/remotes/origin/` prefixes don't matter) or from a `#42` in the message. Commits with neither go under "Other".
4. Unfinished work: the default branch is `git symbolic-ref --short refs/remotes/origin/HEAD` (fall back to `main`, then `master`). Then `git branch --no-merged <default> --format='%(refname:short)'` and keep branches named `<number>-…`.

**Output exactly this** (plain text, ready to paste into the bot's form):

```
What I did today
<3–6 plain sentences. Lead each with the issue, e.g. "#42: …". Say what was achieved, not commit by commit. Mention when a commit says "Closes #n" — that task is finished.>

Plan for tomorrow
<1–3 sentences from the unfinished <number>-… branches that had work today (or recently): what's left on each, as far as the commits show.>

Issues touched: #42, #45
```

Keep it short and factual. If the history doesn't say what's left on a branch, write "continue #42" rather than guessing.
