# Extend the Attendance Bot: GitHub Tasks + End-of-Day Reports

The Mattermost attendance bot already exists and is live (Go, PostgreSQL, Docker Compose, `/att` slash command in four teams). This spec adds **task tracking through GitHub** and **end-of-day (EOD) reports** to that same service.

**Before writing code:** read the existing codebase and its original spec (SPEC.md if present). Follow its existing structure, patterns, naming, config loading, HMAC signing of buttons/dialogs, team-admin checks, audit log, job scheduler with `job_runs` idempotency, and test style. Don't refactor existing features unless needed, and tell me if you do. Work phase by phase (section 12), stop after each phase so I can test, and ask before any decision this spec doesn't cover.

---

## 1. Context

- Mattermost teams (one per organization): **InkYank, Seechanlabs, Bittrix Technologies, CreatorsCult**. Some members belong to several teams. Attendance runs per team; there is also an HQ team with no attendance.
- Existing features: check-in/out, breaks, hours, WFH requests with approve/reject, holidays, member settings, reports, CSV export, fix with audit, scheduled jobs (reminders, auto-close, daily summary at 20:00 IST, weekly report Monday 09:00).
- Code and collaboration live on **GitHub**. The official **Mattermost GitHub plugin** is installed separately by me (for notifications, repo subscriptions, creating issues from messages). **Do not reimplement plugin features.** The bot only reads/writes GitHub data needed for the daily flow below.
- Timezone: Asia/Kolkata (IST). Store UTC.

### How the team actually works (design around this)
- Members write code mostly with **Claude Code and other CLI agents inside the Orca IDE**.
- **One branch per issue**, named with the issue number first: `42-session-timeout`.
- When tests pass, the member **merges the branch into the default branch themselves** (usually locally, without a pull request). Some repos may use PRs later, so support both, but **never assume a PR exists**.
- The final commit landing on the default branch contains `Closes #42`, so GitHub closes the issue, and the Project's built-in workflow moves it to *Done*.
- Agents never post to Mattermost. Mattermost is the human layer: check-ins, discussions, decisions, EOD reports.

## 2. Goals

1. Every task is a GitHub issue on an organization-level **GitHub Project (v2)**.
2. On check-in, the member sees their assigned open tasks and picks what they're working on (moves it to *In progress*).
3. On check-out, the member submits an **EOD report** through a dialog: status updates per task, what they did, blockers, plan for tomorrow. The bot syncs statuses to GitHub, comments on issues, attaches their actual GitHub activity for the day, and posts the report in a daily thread.
4. Admins get EOD views, missing-report tracking, and alerts for overdue, stale, blocked, and idle work.

## 3. GitHub conventions (document in README; I set these up in GitHub)

Each organization has a GitHub Project with these fields, **named exactly**:

| Field | Type | Values |
|---|---|---|
| Status | single select | Todo, In progress, In review, Blocked, Done |
| Priority | single select | High, Medium, Low |
| Due date | date | |
| Estimate | number | hours (optional) |

*In review* stays available but will be rarely used, because most work is merged directly after tests pass.

Non-code work lives in an `operations` repo (or similar) per organization, so every task is an issue. Label `blocked` is added/removed by the bot alongside the Blocked status.

Each Project must have GitHub's built-in workflow **"Item closed → Status: Done"** enabled (and "Pull request merged → Done" for repos that use PRs). The bot relies on this rather than setting Done itself when an issue is closed by a commit.

Repo conventions the bot depends on:
- Branch names start with the issue number and a hyphen: `^(\d+)-`. The bot uses this to link branches to issues.
- Commits that finish a task include `Closes #<n>` (or `Fixes`/`Resolves`) and land on the default branch.

The bot must look up field IDs and option IDs by **name** at startup (GraphQL), cache them, refresh on a schedule or on error, and fail with a clear log message if a field or option is missing — never hard-code IDs.

## 4. GitHub access

- Use a **GitHub App** (not a personal token). Required permissions: Issues (read/write), Organization projects (read/write), Pull requests (read), Contents (read), Metadata (read).
- Support **multiple GitHub orgs/installations**: config maps each Mattermost team to `{github_org, installation_id, project_number}`. Several teams may share one GitHub org and/or project, or each may have its own — design for both.
- Generate installation tokens from the App private key (JWT → installation access token), cache until shortly before expiry.
- Use GraphQL for Projects v2 (read items, update field values) and REST or GraphQL for issues, comments, and activity. Respect rate limits: read `X-RateLimit-Remaining`, back off, and log.
- New env vars: `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY_PATH`, plus per-team installation/project config in the existing team config. Update `.env.example`.

## 5. User mapping

- Add `members.github_login TEXT UNIQUE`.
- `/att github <username>` — member links their own GitHub login. Verify the user exists via the API and is a member of that team's GitHub org; store it.
- `/att github` — show the current link.
- `/att member @user github <username>` — team admin sets it (audited), consistent with the existing `/att member` command.
- Members without a linked login can still check in/out normally; the task parts of the flow are skipped with a one-line hint to run `/att github <username>`.

## 6. Start of day (extends `/att in`)

After a successful check-in (existing behaviour unchanged), if the member has a GitHub login:

1. Fetch their open issues assigned to them on **this team's** project, excluding Status = Done. Sort: overdue first, then Priority (High → Low), then Due date ascending.
2. In the ephemeral check-in reply, add a list (max 10, then "and N more") showing `#number title — Status — Priority — due date`, with ⚠️ on overdue and 🚧 on blocked.
3. Add a **"Pick today's task"** select menu (interactive message select) listing those issues. Choosing one sets its Status to *In progress* on the project (if not already In progress/In review) and replies with confirmation. Members may pick more than one (each pick is one action).
4. Record picks in `task_picks` (section 9) for the EOD dialog.

Check-in must never fail or slow down noticeably because of GitHub: fetch with a short timeout (e.g. 3 s); on timeout/error, check in normally and show "Couldn't load your tasks right now — try `/att tasks`."

## 7. End of day (extends `/att out`)

After a successful check-out (existing behaviour unchanged):

1. Open an **interactive dialog** (needs `trigger_id` from the slash command; when check-out happens via the button, use the action's `trigger_id`) titled "End of day report".
2. Dialog elements:
   - One **select** per relevant issue: options *No change / In progress / In review / Blocked / Done*, default *No change*. Relevant issues = issues picked today + issues currently In progress/In review/Blocked assigned to them on this team's project + issues assigned to them with commits today on a branch matching their number, de-duplicated, max 8 (Mattermost dialogs have element limits; if more, include the 8 most recently updated and note the rest in help text).
   - Issues assigned to them that were **closed today** (usually by a `Closes #n` commit) are not selects; list them in the dialog's introduction text as "✅ Already done today: #42, #51" and record them in `eod_items` with `new_status = 'Done'`, `synced = true` (no GitHub write needed).
   - **What I did today** (textarea, required, min 10 chars)
   - **Blockers** (textarea, optional)
   - **Plan for tomorrow** (textarea, optional)
3. If the member has no GitHub login or no relevant issues, show only the three text fields.
4. On submit:
   1. Save `eod_reports` + `eod_items` in one transaction (unique per member/team/day; resubmitting the same day **updates** the report and edits the existing thread reply, doesn't duplicate).
   2. For each changed item: update the project Status; add/remove the `blocked` label; post an issue comment: `📝 EOD <date> (@mm_username): <new status> — <first 300 chars of "what I did">`.
   3. Fetch the member's GitHub activity for that IST day across the team's GitHub org and store it as a JSON snapshot in `gh_activity`:
      - **Commits on default branches** authored by them (primary metric for this team, since work is merged directly).
      - **Branches worked on**: branches matching `^(\d+)-` with commits by them that day, mapped to issue numbers.
      - **Issues closed** that were assigned to them (including closed by commit keywords).
      - PRs opened/merged and reviews submitted, **only if any exist** (omit from the report when zero).
      Match commits by the member's GitHub login, and document that commits made with a git email not linked to their GitHub account won't be counted. Keep the method simple and document it in the README.
   4. Post the report as a reply in today's **EOD thread** in the team's `#attendance` channel. Create the root post on first report of the day: `📝 EOD reports — Wed 26 Nov` (store root post id in a table keyed by team + day; must be safe under concurrent first submissions).
   5. If Blockers is non-empty or any item → Blocked: post a short alert in `#attendance-admin` with a permalink to the report reply.
5. GitHub steps (ii, iii) run **after** saving and must not block the dialog response: enqueue them, retry with backoff, and mark `sync_status` on the report (`pending/synced/failed`). A failed sync is retried by a job and surfaced in `/att eod`.
6. If the member closes the dialog without submitting, nothing is saved; the report counts as missing. `/att eod submit` reopens the dialog for today (or for yesterday until 11:00 IST next day, for late submissions).

### Report format in the thread

```
📝 Jana — 7h 40m (🏢 office)
✅ Done: #42 Session timeout handling
🔄 In progress: #45 WFH approval buttons
🚧 Blocked: #47 Export CSV — waiting for DB access
Did: <what I did today>
Blockers: <blockers>
Tomorrow: <plan>
GitHub: 6 commits to main · 1 issue closed · branches: 42, 45
```

PR and review counts appear on the GitHub line only when non-zero.

Hours and mode come from the existing attendance calculation for that member/team/day. Omit empty lines. Issue numbers link to the issues.

## 8. Commands

| Command | Who | Behaviour |
|---|---|---|
| `/att tasks` | All | Own open tasks on this team's project (same list as check-in) with the pick select. |
| `/att tasks @user` | Team admin | That member's open tasks. |
| `/att github [username]` | All | Show / link own GitHub login. |
| `/att eod` | All | Own report for today (or "not submitted"). |
| `/att eod submit` | All | Open the EOD dialog (today, or yesterday until 11:00). |
| `/att eod @user [YYYY-MM-DD]` | Team admin | That member's report. |
| `/att eod team [YYYY-MM-DD]` | Team admin | Table: member, hours, submitted ✅/❌, done count, blocked count, sync status. |

Update `/att help` (admin section only for admins). All replies ephemeral, like existing commands.

## 9. Database (new migrations)

```sql
ALTER TABLE members ADD COLUMN github_login TEXT UNIQUE;

CREATE TABLE task_picks (
  mm_user_id  TEXT NOT NULL REFERENCES members,
  team_id     TEXT NOT NULL,
  day         DATE NOT NULL,
  issue_url   TEXT NOT NULL,
  picked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (mm_user_id, team_id, day, issue_url)
);

CREATE TABLE eod_reports (
  id             BIGSERIAL PRIMARY KEY,
  mm_user_id     TEXT NOT NULL REFERENCES members,
  team_id        TEXT NOT NULL,
  day            DATE NOT NULL,
  did_today      TEXT NOT NULL,
  blockers       TEXT,
  tomorrow       TEXT,
  gh_activity    JSONB,
  sync_status    TEXT NOT NULL DEFAULT 'pending' CHECK (sync_status IN ('pending','synced','failed')),
  sync_error     TEXT,
  reply_post_id  TEXT,
  submitted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (mm_user_id, team_id, day)
);

CREATE TABLE eod_items (
  report_id   BIGINT NOT NULL REFERENCES eod_reports ON DELETE CASCADE,
  issue_url   TEXT NOT NULL,
  issue_title TEXT,
  old_status  TEXT,
  new_status  TEXT NOT NULL,
  synced      BOOLEAN NOT NULL DEFAULT false,
  PRIMARY KEY (report_id, issue_url)
);

CREATE TABLE eod_threads (
  team_id       TEXT NOT NULL,
  day           DATE NOT NULL,
  root_post_id  TEXT NOT NULL,
  PRIMARY KEY (team_id, day)
);

CREATE TABLE task_alerts (           -- prevents repeating the same alert
  issue_url   TEXT NOT NULL,
  kind        TEXT NOT NULL,         -- overdue, stale, blocked
  alerted_on  DATE NOT NULL,
  PRIMARY KEY (issue_url, kind, alerted_on)
);
```

Adjust if the existing schema suggests better names; explain changes.

## 10. Scheduled jobs (add to the existing scheduler; skip holidays/weekends like existing jobs)

| Time (IST) | Job |
|---|---|
| Every 15 min | Retry EOD reports with `sync_status` pending/failed (max attempts, then leave failed and alert admins once). |
| 19:30 | DM members who checked out today without an EOD report: "Submit with `/att eod submit`". |
| 20:00 | **Extend** existing daily summary: per member add EOD ✅/❌, tasks done, blocked count; link to the EOD thread. |
| 10:00 | Per team, alert `#attendance-admin`: (a) overdue issues (due date < today, not Done) grouped by assignee; (b) stale issues (In progress with no issue comments/updates **and** no commits on a branch starting with `<number>-` for 3 working days); (c) members with zero open assigned issues. One alert per issue per kind per day (`task_alerts`). |
| Mon 09:00 | **Extend** existing weekly report: per member issues completed, overdue count, EOD submission rate (submitted ÷ days worked). |

## 11. Security & robustness

- GitHub App private key file readable only by the service; never logged.
- Admin checks use the existing team-admin mechanism.
- Every dialog/button carries the existing HMAC-signed context; verify that the submitting user owns the report.
- Members can only change status on issues **assigned to them**; verify assignment server-side before any GitHub write.
- All GitHub calls: timeouts, retries with backoff, rate-limit awareness, structured logs. GitHub being down must never break check-in, check-out, or attendance data.
- Activity metrics are context only: don't build rankings or leaderboards.

## 12. Build phases — stop after each

1. **GitHub foundation:** App auth + installation tokens, team → org/project config, field/option discovery by name, `/att github`, `/att member … github`, `/att tasks` (read-only list).
2. **EOD core:** dialog on check-out, save reports/items, EOD thread posting with idempotent root creation, resubmit/update, `/att eod`, `/att eod submit`.
3. **GitHub sync:** async status updates, labels, issue comments, activity snapshot, retry job, sync status display, blocker alerts.
4. **Start of day:** task list + pick select on check-in and in `/att tasks`, `task_picks`, move to In progress.
5. **Admin & jobs:** `/att tasks @user`, `/att eod @user|team`, 19:30 reminder, 10:00 alerts, daily and weekly report extensions.
6. **Hardening:** integration tests, README update (GitHub App setup, project field setup and built-in workflows, plugin vs bot responsibilities), manual test script like the existing one.
7. **Team repo templates** (files only, no bot code): create `docs/team-workflow/` in this repo containing the three files in section 13, ready for me to copy into each project repo.

## 13. Team repo templates (Phase 7 deliverables)

1. **`CLAUDE.md` template** (also valid as `AGENTS.md` for other agents) — rules for agents working in our repos:
   - Before starting, read the task with `gh issue view <n>`; work only within its scope; note out-of-scope ideas as suggested new issues in the final summary instead of implementing them.
   - Work on a branch named `<n>-short-description`; never commit directly to the default branch.
   - Reference the issue (`#<n>`) in commit messages; the final commit that completes the task includes `Closes #<n>`.
   - Run the test suite before declaring the task done; report failures honestly instead of weakening tests.
   - Never merge, push to the default branch, close issues, change GitHub Project fields, or post to Mattermost — the human does those after checking the work.
   - Leave a placeholder section for project-specific rules (stack, commands, test command).
2. **`.github/ISSUE_TEMPLATE/task.md`** — sections: Context, Acceptance criteria (checklist), Files/areas involved, Out of scope, Notes for agents. Written so the issue works directly as an agent brief.
3. **`.claude/commands/eod.md`** — a Claude Code custom command that drafts the user's EOD text: list today's commits by the current git user across local branches (`git log --since=<today 00:00 local> --author=<git user email> --all`), group them by issue number from branch names and `#n` references, and output 3–6 plain sentences for "What I did today" plus a suggested "Plan for tomorrow" from unfinished branches. It must only summarize; it must not commit, push, or call any API.

## 14. Required tests

- Field/option lookup by name; clear error when a field is missing.
- Relevant-issue selection for the dialog (picked + in-progress, dedupe, limit 8).
- EOD resubmission updates instead of duplicating (report, items, thread reply).
- Concurrent first submissions create exactly one EOD thread root.
- Status update refused for an issue not assigned to the member.
- GitHub timeout during check-in → check-in still succeeds.
- Sync failure → report saved, `sync_status=failed`, retried by job.
- Activity day boundaries use IST.
- Missing-report detection: checked out but no report → reminded; no check-out → not reminded.
- Overdue/stale alerts deduplicated per day.
- Member without GitHub login gets text-only dialog.
- Activity counts commits on the default branch by the member for the IST day; PR/review counts omitted when zero.
- Branch name `42-session-timeout` maps to issue #42; branches without the prefix are ignored for issue mapping.
- Issue closed today by a `Closes #n` commit appears as "already done" in the dialog and is recorded without a GitHub write.
- Stale detection: an In-progress issue with recent commits on its numbered branch is NOT stale.

Use mocked GitHub HTTP responses (recorded fixtures) for unit tests; no real GitHub calls in tests.

## 15. Working style

- Ask before adding dependencies (a GitHub client library like `google/go-github` and a GraphQL client are acceptable if you explain why).
- After each phase: summarize what was built, give a step-by-step manual test in Mattermost (who types what, what they should see), and list anything deferred.
