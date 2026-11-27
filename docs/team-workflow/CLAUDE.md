# Working in this repository

These rules are for AI coding agents (Claude Code reads this file as `CLAUDE.md`; other agents read the same text as `AGENTS.md`) and for people. Our attendance bot reads issues, branches and commits, so following them keeps everyone's tasks and end-of-day reports accurate.

## Every task is a GitHub issue

- **Read the task first:** `gh issue view <n>` (and its comments: `gh issue view <n> --comments`). The issue is the brief — its *Acceptance criteria* say what "done" means.
- **Stay in scope.** Do only what the issue asks. If you notice other bugs, refactors or ideas, don't do them: list them at the end of your summary under **Suggested new issues**, one line each.
- If the issue is unclear or seems wrong, stop and ask instead of guessing.

## Branches and commits

- Work on a branch named **`<n>-short-description`**, e.g. `42-session-timeout` (issue number first, lowercase, hyphens). Create it from the latest default branch: `git switch -c 42-session-timeout origin/main`.
- **Never commit to the default branch** (`main`/`master`).
- Mention the issue in every commit message: `Handle expired sessions (#42)`.
- The final commit that completes the task includes **`Closes #42`** on its own line. When it reaches the default branch, GitHub closes the issue and the Project moves it to Done.
- Commit with the email that's linked to your GitHub account, or your commits won't count as yours.

## Done means tested

- Run the project's tests (see *Project specifics* below) before saying the task is done.
- If tests fail, say so plainly, with the output. **Never** weaken, skip or delete a test to make it pass, and never claim something works that you didn't run.
- Your final summary lists: what changed (files), how you tested it, anything left undone, and *Suggested new issues*.

## Things only a human does

Agents must **not**:

- merge pull requests or branches, or push to the default branch;
- close or reopen issues, or edit their labels, assignees or milestones;
- change GitHub Project fields (Status, Priority, Due date, …) — the team member reports status in the end-of-day report;
- post in Mattermost or anywhere else outside this repository;
- force-push, rewrite published history, or delete branches.

Pushing your own `<n>-…` branch and opening a **draft** pull request is fine when you're asked to.

## Project specifics

<!-- Fill this in for each repository. -->

- **What this project is:** …
- **Stack:** … (language, framework, database)
- **Setup:** `…` (install dependencies, env file)
- **Run locally:** `…`
- **Test command:** `…` (must pass before a task is done)
- **Lint / format:** `…`
- **Layout:** where the main code, tests and config live
- **Rules for this repo:** e.g. coding style, files never to edit, how migrations are written
