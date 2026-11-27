# Team repo templates

Three files to copy into **each project repository** (e.g. a team's app repo or `operations`). They make people and AI coding agents work the way the attendance bot expects: one issue per task, branches named after the issue, `Closes #n` to finish — so task lists, end-of-day reports, stale checks and the Project board stay accurate.

| File | Goes to (in the project repo) | What it does |
|---|---|---|
| [`CLAUDE.md`](CLAUDE.md) | `CLAUDE.md` (and a copy as `AGENTS.md`) | House rules for AI agents and people: read the issue, stay in scope, branch `<n>-…`, `Closes #n`, run tests, never merge or change the board. Has a **Project specifics** section to fill in. |
| [`.github/ISSUE_TEMPLATE/task.md`](.github/ISSUE_TEMPLATE/task.md) | `.github/ISSUE_TEMPLATE/task.md` | The *New issue → Task* form: Context, Acceptance criteria, Files/areas, Out of scope, Notes for agents. A filled-in issue works directly as an agent's brief. |
| [`.claude/commands/eod.md`](.claude/commands/eod.md) | `.claude/commands/eod.md` | A `/eod` command in Claude Code that drafts *What I did today* and *Plan for tomorrow* from today's commits. Read-only: it never commits, pushes or calls GitHub/Mattermost. |

## Copy them into a repo

From the project repository's root (adjust the path to where this repo is checked out):

```bash
T=../Mattermost-attendance/docs/team-workflow
mkdir -p .github/ISSUE_TEMPLATE .claude/commands
cp "$T/CLAUDE.md" CLAUDE.md
cp "$T/CLAUDE.md" AGENTS.md                      # same rules for other agents
cp "$T/.github/ISSUE_TEMPLATE/task.md" .github/ISSUE_TEMPLATE/task.md
cp "$T/.claude/commands/eod.md" .claude/commands/eod.md
```

Then:

1. Fill in **Project specifics** at the end of `CLAUDE.md` (and `AGENTS.md`): stack, setup, test command. If the repo already has a `CLAUDE.md`, merge the rules into it instead of overwriting.
2. Commit on a branch and merge it like any other change: `git switch -c 0-team-workflow`, commit, open a pull request.
3. Check: on GitHub, *New issue* offers **Task**; in Claude Code, typing `/eod` shows the command.

## How a task flows

1. Someone creates an issue with the **Task** template and assigns it. With the Project's *Auto-add* workflow it lands on the board as **Todo**.
2. At `/att in`, the assignee picks it → **In progress**.
3. Work happens on branch `42-short-description`; commits mention `#42`. (With Claude Code: "Work on issue #42" — the agent follows `CLAUDE.md`.)
4. A human reviews and merges; the commit with `Closes #42` closes the issue → **Done**.
5. At `/att out`, the member runs `/eod` in Claude Code, pastes the draft into the bot's form and sets statuses. The bot writes them to GitHub.
