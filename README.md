# Mattermost Attendance Bot

Self-hosted attendance for Mattermost: check-in/out, breaks, hours, WFH approvals and reports — all through the `/att` slash command. Full spec: [attendance-bot-prompt.md](attendance-bot-prompt.md).

**Status: attendance complete; GitHub tasks + EOD reports in progress (phase 3 of 7: end-of-day reports synced to GitHub, spec: tasks-eod-prompt.md).** Available now: check in/out, breaks, buttons, `/att status`, custom status, `/att hours`, scheduled reminders, daily summaries, auto-close, holidays, per-member settings, admin reports, `/att fix`, CSV export, the Monday weekly report and WFH requests with approvals.

## How it works

- Go service (`attendance`) + its own Postgres 16 (`attendance-db`), via Docker Compose.
- The service joins Mattermost's Docker network and is reached by Mattermost at `http://attendance:8080`. **No ports are published.**
- Times are stored in UTC; days and display use `TIMEZONE` (default `Asia/Kolkata`).
- Migrations in `migrations/` run automatically on startup.

## Setup

### 1. Mattermost (one-time, as a system admin)

1. **System Console → Integrations → Bot Accounts:** enable bot account creation.
2. **Integrations → Bot Accounts → Add Bot:** username `attendance`, role **System Admin** (needed later to set members' custom status). Copy the access token → `MM_BOT_TOKEN`.
3. **System Console → Environment → Developer → Allow untrusted internal connections to:** add `attendance`.
4. **System Console → Integrations → Integration Management:** make sure *Enable Custom Slash Commands* is on.
5. In **each of the four organization teams** (InkYank, Seechanlabs, Bittrix Technologies, CreatorsCult — **not HQ**):
   - **Integrations → Slash Commands → Add:**
     - Title `Attendance`, trigger word `att`
     - Request URL `http://attendance:8080/mm/command`, method **POST**
     - Autocomplete on, hint `in | out | break | back | status | hours | wfh | holiday | help` (admin commands are listed in `/att help`)
     - Copy the **token** → `TEAM_<KEY>_SLASH_TOKEN`
   - Create public channel `attendance` and private channel `attendance-admin` (add the admins). Their ids go in `.env` (needed from phase 3).
   - Add the `attendance` bot to the team and both channels.
6. Team ids: **System Console → User Management → Teams → <team>** (id in the URL), or `GET /api/v4/teams/name/<slug>`.

### 2. Server

```bash
git clone https://github.com/vairabarath/Mattermost-attendance.git
cd Mattermost-attendance
cp .env.example .env        # fill in every value
docker network ls           # find Mattermost's network → MM_DOCKER_NETWORK
docker compose up -d --build
docker compose logs -f attendance
```

`MM_URL` must be Mattermost's address **from inside Docker**, e.g. `http://mattermost:8065` (the Mattermost container's service name and port), not the public URL.

Check health: `docker compose ps` should show `attendance` as *healthy*.

## Environment variables

See [.env.example](.env.example). Required: `MM_URL`, `MM_BOT_TOKEN`, `POSTGRES_PASSWORD`, `HMAC_KEY` (≥ 32 chars), `TEAMS` and, for each team key, `TEAM_<KEY>_ID`, `TEAM_<KEY>_NAME`, `TEAM_<KEY>_SLASH_TOKEN`.

Teams not listed in `TEAMS` (e.g. HQ) get a friendly "attendance isn't used here" reply. A slash token is only accepted together with its own team's id.

## Commands

| Command | What it does |
|---|---|
| `/att in [note]` | Check in to this team. One open session across all teams. Reply has ☕ Break / 🔴 Check out buttons. |
| `/att out [summary]` | Check out (from any team); shows this session and today's total. Closes an open break. |
| `/att break` / `/att back` | Start / end a break. Breaks are not counted as work. |
| `/att status` | Who is checked in to this team right now, since when, 🏢/🏠, and who is on break. |
| `/att hours [today\|week\|month]` | Your hours: per day, total, and per organization. Week = Monday–today, month = 1st–today. |
| `/att holiday list` | Upcoming holidays for this team (and all-org ones). |
| `/att wfh` | Request work from home: opens a form (start date, optional end date, half day, reason). |
| `/att wfh list` | Your WFH requests in this team and their status. |
| `/att wfh cancel <id>` | Cancel a pending request, or an approved one that hasn't started. |
| `/att github` | Show your linked GitHub login. Linking is done by an admin (below), so nobody can claim someone else's GitHub account. |
| `/att tasks` | Your open issues on this team's GitHub Project (not Done): overdue first, then priority, then due date. ⚠️ overdue, 🚧 blocked. With a **Pick today's task** menu (see [Start of day](#start-of-day)). |
| `/att eod` | Your end-of-day report for today in this team. |
| `/att eod submit` | Open the end-of-day form: for today, or for yesterday until 11:00. |
| `/att help` | Command list (admin commands shown to admins only). |

**Admin commands** — for system admins (any team) and team admins (their own team only):

| Command | What it does |
|---|---|
| `/att member @user` | Show the member's settings. |
| `/att member @user tracked on\|off` | `off` excludes them from reminders, "not checked in" lists and expected-hours comparisons. |
| `/att member @user reminders on\|off` | The 10:30 reminder DM. |
| `/att member @user hours <n>` | Expected hours per day, all organizations combined (default 8; quarter hours). |
| `/att member @user github <username>\|none` | Link (verified against this team's GitHub org) or unlink a member's GitHub login. Audited. The bot can't prove the account belongs to the person, so copy the username they send you. |
| `/att holiday add YYYY-MM-DD <name>` | Holiday for this team. |
| `/att holiday add YYYY-MM-DD all <name>` | Holiday for all organizations (system admins only). |
| `/att holiday remove <id>` | Remove a holiday (ids from `/att holiday list`). |
| `/att report @user [week\|month]` | That member's hours **in this team** per day (hours, 🏢/🏠, breaks, auto-closed), plus all-orgs total and expected hours. Default `week`. |
| `/att report team [week\|month]` | One row per member: days, hours in this team, hours across all orgs, expected, WFH days, auto-closed. ⚠️ = all-orgs under 90% of expected. Expected counts workdays from the day the bot first saw the member, up to yesterday. |
| `/att fix @user YYYY-MM-DD in HH:MM out HH:MM <reason>` | Replace all the member's sessions **in this team** that started that day with one session (no breaks). See rules below. |
| `/att wfh pending` | Pending WFH requests for this team. |
| `/att wfh all` | Every WFH request in this team from the last 30 days and upcoming (any status), with who is WFH today. |
| `/att export [YYYY-MM]` | CSV of this team's sessions for the month (default: this month), sent to you as a DM from the bot. |
| `/att tasks @user` | That member's open tasks on this team's GitHub Project (same list they see). |
| `/att eod @user [YYYY-MM-DD]` | That member's end-of-day report in this team (default today), with a link to it in the thread — or whether they worked without sending one. |
| `/att eod team [YYYY-MM-DD]` | Everyone who worked in this team that day (or sent a report): hours, EOD ✅/❌ (⏳ = still checked in), Done and Blocked counts, GitHub sync, and a link to the day's thread. |

### WFH flow

1. `/att wfh` opens a form. It's validated on submit (valid dates, end ≥ start, not in the past, at most 31 days, no overlap with your pending/approved requests in this team) and saved as pending.
2. The request is posted to the team's `#attendance-admin` with **✅ Approve / ❌ Reject** buttons, the reason and the member's approved WFH days this month. ⚡ marks requests for today, or for tomorrow made after 20:00.
3. Only admins of that team can decide. Two admins clicking at once produce exactly one decision (the other sees "Already approved by …"). Reject asks for a reason. The post is updated with who decided and when; the member gets a DM; the audit log records it.
4. On an approved **full** day, `/att in` in that team records 🏠 and sets 🏠 *Working from home*; if approved for today while the member is checked in, the open session switches to 🏠. `/att fix` on an approved day also uses 🏠. **Half days are informational** — the day stays 🏢. There is no monthly limit.
5. Cancelling updates the admin post to "Cancelled by member". At 18:00 on workdays, still-pending requests covering tomorrow are listed in `#attendance-admin`.

WFH buttons stay valid for 60 days (the expiry is part of the signed payload); dialogs submit to `/mm/dialog` with signed state bound to the user.

**`/att fix` rules:** 24-hour times on the same date (no overnight fixes); no future dates or future `out`; a reason is required; refused if the member still has an open session in this team from that day, or if the new times overlap another of their sessions (another org, or one crossing midnight). Old and new sessions are written to the audit log in the same transaction. Mode is 🏠 if the member has an approved full-day WFH request for that date in this team, otherwise 🏢.

**CSV columns:** `username, display_name, date, check_in, check_out, break_minutes, net_minutes, net_hours, work_mode, auto_closed, in_note, out_summary`. Times are in `TIMEZONE`. Notes starting with `= + - @` are prefixed with `'` so spreadsheets don't run them as formulas.

Member settings are global (one person, one set of settings) and every change is written to the audit log. The target must be a member of the team the command is run in.

## GitHub tasks

Each team can be linked to a GitHub organization and an organization **Project (v2)**. An admin links each member's GitHub login once (`/att member @user github <username>`; one login per member, used in all teams; admins may also link themselves with `/att github <username>`) and see their assigned open tasks with `/att tasks`. GitHub problems never affect attendance: on any error the task part is skipped with a short message.

### One-time GitHub setup

1. **Create a GitHub App** (org → Settings → Developer settings → GitHub Apps → New GitHub App). Homepage URL: anything (e.g. your Mattermost URL). **Webhook: untick "Active".** Permissions:
   - Repository: **Issues** read & write, **Pull requests** read, **Contents** read, **Metadata** read.
   - Organization: **Projects** read & write, **Members** read.
   - "Where can this GitHub App be installed?": *Any account* if your teams use more than one GitHub org, otherwise *Only on this account*.
2. Note the **App ID**, then **Generate a private key** (downloads a `.pem`).
3. **Install** the App on each GitHub org (App page → Install App), on *All repositories*.
4. On the server, put the key at `secrets/github-app.pem` next to `docker-compose.yml`, owned by the container user and readable only by it:
   ```bash
   sudo mkdir -p secrets && sudo mv ~/your-app.private-key.pem secrets/github-app.pem
   sudo chown -R 65532:65532 secrets && sudo chmod 500 secrets && sudo chmod 400 secrets/github-app.pem
   ```
5. In `.env`: `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY_PATH=/run/secrets/github-app.pem`, and per team `TEAM_<KEY>_GITHUB_ORG` / `TEAM_<KEY>_GITHUB_PROJECT` (the number in `github.com/orgs/<org>/projects/<number>`). Several teams may share an org and/or a Project. Then `docker compose up -d --build attendance`; the log shows `github project ok` (or exactly what's missing) for each team.

### Project fields (named exactly)

| Field | Type | Values |
|---|---|---|
| Status | single select | Todo, In progress, In review, Blocked, Done |
| Priority | single select | High, Medium, Low |
| Due date | date | |
| Estimate | number | hours (optional field) |

The bot looks fields and options up **by name** (never hard-coded ids), caches them for 6 hours, and logs a clear error if one is missing. Renaming or recreating an option gives it a new id: the bot copes, but GitHub's own workflows that set that option must be pointed at it again.

### Project workflows (Project → ⋯ → Workflows)

| Workflow | Setting | Why |
|---|---|---|
| **Item added to project** | Status: **Todo** | new tasks start in Todo |
| **Item closed** | Status: **Done** | a `Closes #42` commit or a closed issue finishes the task |
| **Pull request merged** | Status: **Done** | for repos that use pull requests |
| **Auto-add to project** | repo `operations`, filter `is:issue is:open` | optional — the bot adds assigned issues itself (below); keep it if you also want unassigned `operations` issues on the board |

Put non-code work (admin, finance, hiring, client) in an `operations` repo so every task is an issue.

### One board per organization, fed from every repo

Each team has **one** Project for its whole GitHub org; issues from all its repos go there. Use the Project's **views** for per-repo or per-person tabs (e.g. a view filtered to `repo:<org>/<repo>`, or grouped by *Repository* or *Assignees*).

**The bot adds assigned issues to the board** (`GITHUB_ADD_ASSIGNED_TO_BOARD`, default on). An issue is added to a team's Project when it is an **open issue** in a **non-archived repo of the team's org**, **assigned to a member with a linked GitHub login**, and not on the Project yet. It's added with Status **Todo** (an existing Status is kept). Unassigned issues, closed issues and pull requests are never added.

- **When a member looks — instantly:** `/att in`, `/att tasks` and the EOD form list such issues right away (marked 🆕 *added to the board*) and add them in the background; picking one adds it and moves it to In progress.
- **In the background — within `GITHUB_BOARD_SWEEP_MINUTES` (default 2):** the bot searches the org for open issues updated since its last run (creating or assigning an issue counts as an update) and adds the ones that qualify. A full pass over all open issues runs at startup and daily at 03:00. One GitHub search per org per run; failures are logged and the next run catches up. `0` turns the sweep off (members' commands still add issues).
- New repos need no setup for this: the App is installed on *All repositories*, so their issues are picked up as soon as they're assigned.
- If two teams ever share one org with **different** Projects, nothing is added for them (the bot can't tell which board an issue belongs to) — add those by hand.

**Team repo templates:** [`docs/team-workflow/`](docs/team-workflow/README.md) has a `CLAUDE.md`/`AGENTS.md` with these rules for people and AI agents, a GitHub *Task* issue template, and a `/eod` Claude Code command that drafts the end-of-day text from today's commits — copy them into each project repo.

Repo conventions: one branch per issue named `<number>-description` (e.g. `42-session-timeout`) — the EOD activity line and the stale check read these; the final commit on the default branch says `Closes #42`. Commits count only when the commit email is linked to the member's GitHub account.

### Adding GitHub for another team

The App is installable on any account, so other organizations reuse it:

1. Create the GitHub organization (owner: an admin), invite the team's members, and move or create its repos there (including an `operations` repo). To bring an existing repo from someone's personal account: repo → Settings → *Transfer ownership* → the org (issues, PRs, branches and history move with it; the old URL redirects). Its open assigned issues land on the board by themselves.
2. Create an organization **Project**, with the fields and workflows above.
3. **Install the App** on the new org: App page → *Install App* → the org → *All repositories*.
4. Add `TEAM_<KEY>_GITHUB_ORG` and `TEAM_<KEY>_GITHUB_PROJECT` to `.env`, then `docker compose up -d --build attendance`. Check the log for `github project ok` for that team.
5. Link each member: `/att member @user github <username>` (the bot checks they're in that org). Note: a member already linked by another team's admin is used as-is in this team too — see *Known limitations*.
6. Test: a member runs `/att tasks` and sees an issue assigned to them.

### Mattermost GitHub plugin vs this bot

They do different jobs and can run side by side:

| | Mattermost GitHub plugin | This bot |
|---|---|---|
| Who it acts as | each person's own GitHub account (OAuth) | one GitHub App for the organization |
| Notifications | PR reviews, mentions, assignments, CI — per person, in DMs and channel subscriptions | none |
| Daily work | sidebar counts, `/github todo`, creating issues from posts | the task list at check-in, picking today's task, the end-of-day report and its sync to the Project |
| Project (v2) board | not used | reads and updates Status, Priority, Due date |
| Reports and alerts | none | EOD thread, blocker alerts, 10:00 task check, task columns in the daily and weekly reports |

Use the plugin for notifications and code review; use the bot for attendance, tasks and reports. Neither needs the other.

## Start of day

After a successful `/att in`, a member with a linked GitHub login also sees their open tasks on the team's Project (the `/att tasks` list) and a **Pick today's task** menu. Picking a task:

- moves it to **In progress** on the Project (adding it to the Project if needed) — unless it's already In progress, **In review** or **Blocked**, which are left as they are (a pick never moves work backwards);
- records the pick for the day of the member's open session, so the task gets its own dropdown in that day's end-of-day form, even if it was Todo.

Members can pick several tasks, one at a time, and pick more later from `/att tasks`. The bot checks on GitHub that the issue is assigned to the member and open before changing anything. GitHub never slows down or blocks a check-in: the task list waits at most 3 s, and on any error the check-in reply says *Couldn't load your tasks right now — try `/att tasks`.* Members without a linked login see check-in as before.

## End-of-day reports

Checking out (`/att out` or the 🔴 button) opens an **End of day report** form for the team and day of the session:

- one dropdown per relevant task (*No change / In progress / In review / Blocked / Done*): tasks picked that day plus tasks currently In progress, In review or Blocked on the team's Project, at most 8 (the most recently updated; the form says how many were left out);
- issues assigned to you that were **closed today** (usually by a `Closes #n` commit) are listed as "✅ Already done today" and recorded as Done;
- **Other tasks I finished** (multi-select): the member's other open tasks, Todo included (up to 25), so a finished task that had no dropdown can be reported; each one picked is recorded as Done;
- **What I did today** (required, at least 10 characters), **Blockers** and **Plan for tomorrow** (optional).

Without a linked GitHub login, or if GitHub doesn't answer within 1.5 s, the form opens with only the three text fields. Closing the form saves nothing; the check-out reply has a **📝 End-of-day report** button (and `/att eod submit`) to reopen it.

On submit the report is saved and posted as a reply in the team's daily thread in **#attendance** (`📝 EOD reports — Thu 26 Nov`, created by the first report of the day — exactly once, even with simultaneous submissions), with the member's hours and 🏢/🏠 for that day. Submitting again the same day updates the report and edits the same reply. `@channel`/`@all`/`@here` in report text don't notify anyone.

### Syncing to GitHub

Right after saving (in the background, never blocking the form), each **changed** task is written to GitHub:

1. the bot checks the issue is **assigned to the member** and in the team's org; otherwise it's skipped (noted in `/att eod`);
2. sets the issue's **Status** on the team's Project (adding it to the Project if needed);
3. adds the `blocked` label for Blocked, removes it otherwise (the label is created in the repo if missing);
4. **closes** the issue for Done (as completed);
5. comments `📝 EOD 2026-11-26 (@jana): **Done** — <first 300 characters of "What I did">` (as the App, `<app>[bot]`; `@` in the text doesn't mention GitHub users).

Then it attaches the member's **GitHub activity** for that IST day, shown as the report's last line, e.g. `GitHub: 6 commits to default branches · 1 issue closed · branches: 42, 45` (PR and review counts only when non-zero):

- **commits**: GitHub commit search in the org, by author, committed during the day. Commit search only covers **default branches**, which fits merging locally into `main`. Commits are matched by GitHub login, so commits made with a git email that isn't added to the member's GitHub account **aren't counted** (`git config user.email` must be one of their GitHub emails);
- **branches**: in up to 10 repos pushed that day, branches named `<issue>-…` with the member's commits that day (`42-session-timeout` → #42; other names are ignored);
- **issues closed** assigned to them; **PRs** opened / merged; PRs they **reviewed** that were updated that day (an approximation).

A failed sync never loses the report: `sync_status` becomes `failed`, `/att eod` shows why, and a job retries every 15 minutes (6 tries in total, then admins get one message in #attendance-admin). Only one sync of a report runs at a time (a 5-minute lease). Resubmitting resets the tries.

### Blocker alerts

A report with **Blockers** filled in or a task set to **Blocked** posts an alert in **#attendance-admin** with the blocker text, the blocked tasks and a link to the report. Resubmitting edits the same alert (or marks it "no longer lists blockers").

## Scheduled jobs

Times are in `TIMEZONE`. Daily jobs skip non-workdays (`WORKDAYS`) and holidays — a team holiday skips that team, an all-org holiday skips everyone.

| When | Job |
|---|---|
| 10:00 | **Task check** per team with a GitHub Project, in `#attendance-admin` (see below). Nothing is posted when there's nothing to report. |
| 10:30 | DM tracked members (reminders on) who haven't checked in to **any** team today. One DM per person. |
| 19:00 | DM everyone still checked in. |
| 19:30 | DM tracked members who checked out of a team today without sending that team's end-of-day report (one DM listing the teams). People still checked in aren't reminded: the form opens when they check out. |
| 20:00 | Daily summary in each team's `#attendance`: who worked (hours, 🏢/🏠, **EOD ✅/❌/⏳, tasks Done, Blocked**), a link to the day's EOD thread, who is still checked in, who worked for another org instead, who didn't check in. |
| 18:00 | Pending WFH requests covering tomorrow, per team, in `#attendance-admin` (only if there are any). |
| Mon 09:00 | Weekly report for the previous Mon–Sun in each team's `#attendance-admin` (same table as `/att report team`), plus a **Tasks and end-of-day reports** table: EOD reports sent ÷ days worked, issues completed (assigned to them, closed that week, in the team's org) and overdue now. Issue columns only for members with a linked GitHub login. Runs even if Monday is a holiday. |
| Every 2 min (`GITHUB_BOARD_SWEEP_MINUTES`), plus a full pass at startup and 03:00 | Add open issues assigned to linked members to their team's Project (see *One board per organization*). Runs every day. |
| Hourly (:05), every day | Auto-close sessions open longer than `MAX_SESSION_HOURS`: check-out is set so net time = the member's expected hours (never later than now). DM to the member, flag in `#attendance-admin`, audit log, custom status restored. |

**Task check (10:00).** Only issues assigned to tracked members with a **linked GitHub login** count; issues of other GitHub users are ignored.

- ⚠️ **Overdue:** open, not Done, due date before today — grouped by member.
- 🐢 **Stale:** *In progress* with no issue update (comments, edits, labels) since the start of the day 3 working days ago **and** no commit since then on a branch of its repo named `<number>-…` (first 100 branches). If the branch check fails, the issue isn't called stale.
- 😴 **No open tasks:** linked members with no open task on the Project.

Each team's check runs once a day (`job_runs`); if GitHub fails it retries twice (after 30 s and 2 min), then skips that day.

Members are read from each team's Mattermost member list (bots and deactivated users excluded) and registered automatically, so people who never used `/att` still show as "not checked in". Each run is recorded in `job_runs`, so a restart never sends anything twice; a daily job missed during a restart still runs if the service is back within 2 hours.

**Custom status:** on check-in the bot sets 🟢 *Working* (🏠 *Working from home* for WFH), ☕ *On break* during breaks. Your own status is saved at check-in and restored at check-out (unless it has expired, or you changed your status yourself meanwhile). Status failures never block check-in/out.

**Buttons** are HMAC-signed (`HMAC_KEY`), only work for the member they were shown to, and expire after 48 hours.

## Development and tests

```bash
go vet ./...
go test ./...                      # unit tests; integration tests are skipped
./scripts/test-integration.sh      # everything, incl. integration tests (needs Docker only)
```

**Integration tests** (`internal/store/integration_test.go`) run against a real Postgres when `TEST_DATABASE_URL` is set. Each test creates its own throwaway schema, applies the real migrations and drops it afterwards, so they never touch existing data. `scripts/test-integration.sh` starts a temporary Postgres (`docker-compose.test.yml`, tmpfs, its own network) and runs `go test` in a Go container — nothing needs to be installed but Docker.

**GitHub Actions** (`.github/workflows/test.yml`) runs on every push and pull request: gofmt, `go vet`, all tests with the race detector against a Postgres service, and `govulncheck`.

Unit tests cover the hours calculation, week/month ranges, custom status save/restore, button signing, config loading, slash-token verification, the admin check, auto-close math, job idempotency, reminders, workdays/holidays, the daily summary rules, team/user reports, expected hours with holidays, `/att fix` parsing, CSV export (incl. formula escaping) and the weekly report — and for tasks and EOD: the GitHub client against a fake GitHub (App auth, token caching, field discovery by name, search paging, rate limits, every write in order, assignment checks), the EOD form and its limits, picks, sync retries and blocker alerts, the admin views and the 19:30/10:00 jobs.

Integration tests cover the database rules: one open session across teams, breaks, midnight splits, `/att fix`, auto-close, concurrent WFH decisions, job idempotency, member settings, GitHub login uniqueness, EOD resubmits, exactly one EOD thread root and one reply under concurrent submits, the sync lease (incl. taking over an expired one), task picks, EOD statuses, and re-running migrations.

**Manual test script:** [`docs/manual-test.md`](docs/manual-test.md) — a step-by-step check in Mattermost after a deploy.

## Backups

`scripts/backup.sh` writes a compressed `pg_dump` (custom format) to `/var/backups/attendance/attendance-YYYY-MM-DD_HHMM.dump` (directory 700, files 600 — they contain personal data), verifies it with `pg_restore --list`, and deletes backups older than 14 days (`KEEP_DAYS`). It exits non-zero on failure.

Installed on the server as `/etc/cron.d/attendance-backup`:

```
# Daily attendance DB backup at 21:30 UTC (03:00 IST)
30 21 * * * root /var/www/mattermost-attendance/scripts/backup.sh >> /var/log/attendance-backup.log 2>&1
```

**Restore** (takes a safety backup of the current DB first, stops the bot, restores, starts it):

```bash
cd /var/www/mattermost-attendance
ls -lh /var/backups/attendance/
./scripts/restore.sh /var/backups/attendance/attendance-2026-11-25_2130.dump --yes
```

Backups live on the same VPS, so they protect against mistakes and database corruption but **not** against losing the server. To keep off-server copies, copy `/var/backups/attendance/` elsewhere (e.g. `rsync`/`rclone` in the same cron job).

## Security

Reviewed at the end of phase 6:

- **Slash commands:** per-team token, constant-time compare; a token is only valid with its own team id; HQ and unknown teams are refused.
- **Buttons and dialogs:** HMAC-SHA256 over all fields (`HMAC_KEY`), with expiry (48 h; WFH buttons 60 days via a signed `exp`). Personal buttons and every dialog are bound to the member they were made for; a select menu's chosen value (added by Mattermost outside the signature) is treated as untrusted input — a picked task must be an issue URL in the team's org and assigned to the member on GitHub; WFH buttons are bound to a team and the clicker's admin rights are checked.
- **Admin rights** are always looked up via the Mattermost API (system admin, or team admin via `scheme_admin`/`team_admin`), cached 60 s, never taken from the request.
- **No published ports:** the bot is only on Docker networks (`mattermost_default` with Mattermost and its Postgres, and its own network with `attendance-db`). The bot's database has no network path from Mattermost's network.
- **SQL:** all values are bound parameters; the only concatenation is of constant column lists.
- **Logs:** JSON; only ids, team/job names, timings and errors — never tokens, notes, reasons or message text, and never request bodies.
- **Secrets** only in `.env` (mode 600, git-ignored, not in git history). The image is distroless and runs as `nonroot`. Request bodies are capped at 64 KB, with server timeouts.
- **GitHub App key:** mounted read-only from `secrets/` (git-ignored), mode 400, owned by the container user; never logged. The bot **refuses** a key file that group or others can read (GitHub features stay off, with the reason in the log). Installation tokens are cached in memory only.
- **GitHub writes** happen only on issues **assigned to the member**, in the team's org, checked on GitHub right before each write (EOD statuses and picks). A pick must also be on the team's Project. Values from forms and select menus are never trusted: issue lists come from the signed form state, a picked issue is re-checked.
- **GitHub calls** have timeouts, retries with backoff on 5xx (never for comments and other writes that can't be repeated), and after a rate limit the org's calls pause until GitHub's reset time (at most an hour). GitHub problems never block check-in, check-out or saving a report.
- **EOD reports:** a signed form or button still works for 48 h, but a report can only be sent or changed the same day or until 11:00 the next morning — checked when it's used. Each sync claims a 5-minute lease and finishes within 4 minutes; a report resubmitted during a sync is synced again, never marked synced by the older run. Issue titles and member text can't notify a whole channel (`@channel`, `@all`, `@here` are neutralised).
- **Dependencies:** pgx and robfig/cron only (the GitHub client is hand-written on the standard library); `govulncheck` reports no known vulnerabilities (also run in CI).
- **CSV export** escapes values starting with `= + - @` so spreadsheets don't run them.

## Known limitations

- The system can't verify a member's physical location; WFH/office mode reflects approvals, not location.
- Leave isn't tracked yet: there is only a hook, so people on leave still get reminders and show as "not checked in".
- Anything that can reach the bot on the Docker network could send it a slash command with a stolen token, or replay a signed button within its lifetime (the user id in click payloads is trusted). Only Mattermost, its Postgres and the bot are on `mattermost_default` today — keep it that way and never publish port 8080.
- **One GitHub login per member, for all teams.** An admin of one team links it after checking membership of *that* team's org; other teams' orgs trust the same link. With one GitHub org (today) this changes nothing. Before connecting a second org, decide whether links should be per team (see *Adding GitHub for another team*).
- Mattermost doesn't sign requests to `/mm/*`. Slash commands are verified by per-team token; the service must stay reachable **only** on the internal Docker network — never publish its port.
