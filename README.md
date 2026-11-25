# Mattermost Attendance Bot

Self-hosted attendance for Mattermost: check-in/out, breaks, hours, WFH approvals and reports — all through the `/att` slash command. Full spec: [attendance-bot-prompt.md](attendance-bot-prompt.md).

**Status: Phase 5 (WFH).** Available now: check in/out, breaks, buttons, `/att status`, custom status, `/att hours`, scheduled reminders, daily summaries, auto-close, holidays, per-member settings, admin reports, `/att fix`, CSV export, the Monday weekly report and WFH requests with approvals.

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
| `/att help` | Command list (admin commands shown to admins only). |

**Admin commands** — for system admins (any team) and team admins (their own team only):

| Command | What it does |
|---|---|
| `/att member @user` | Show the member's settings. |
| `/att member @user tracked on\|off` | `off` excludes them from reminders, "not checked in" lists and expected-hours comparisons. |
| `/att member @user reminders on\|off` | The 10:30 reminder DM. |
| `/att member @user hours <n>` | Expected hours per day, all organizations combined (default 8; quarter hours). |
| `/att holiday add YYYY-MM-DD <name>` | Holiday for this team. |
| `/att holiday add YYYY-MM-DD all <name>` | Holiday for all organizations (system admins only). |
| `/att holiday remove <id>` | Remove a holiday (ids from `/att holiday list`). |
| `/att report @user [week\|month]` | That member's hours **in this team** per day (hours, 🏢/🏠, breaks, auto-closed), plus all-orgs total and expected hours. Default `week`. |
| `/att report team [week\|month]` | One row per member: days, hours in this team, hours across all orgs, expected, WFH days, auto-closed. ⚠️ = all-orgs under 90% of expected. Expected counts workdays from the day the bot first saw the member, up to yesterday. |
| `/att fix @user YYYY-MM-DD in HH:MM out HH:MM <reason>` | Replace all the member's sessions **in this team** that started that day with one session (no breaks). See rules below. |
| `/att wfh pending` | Pending WFH requests for this team. |
| `/att wfh all` | Every WFH request in this team from the last 30 days and upcoming (any status), with who is WFH today. |
| `/att export [YYYY-MM]` | CSV of this team's sessions for the month (default: this month), sent to you as a DM from the bot. |

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

## Scheduled jobs

Times are in `TIMEZONE`. Daily jobs skip non-workdays (`WORKDAYS`) and holidays — a team holiday skips that team, an all-org holiday skips everyone.

| When | Job |
|---|---|
| 10:30 | DM tracked members (reminders on) who haven't checked in to **any** team today. One DM per person. |
| 19:00 | DM everyone still checked in. |
| 20:00 | Daily summary in each team's `#attendance`: who worked (hours, 🏢/🏠), who is still checked in, who worked for another org instead, who didn't check in. |
| 18:00 | Pending WFH requests covering tomorrow, per team, in `#attendance-admin` (only if there are any). |
| Mon 09:00 | Weekly report for the previous Mon–Sun in each team's `#attendance-admin` (same table as `/att report team`). Runs even if Monday is a holiday. |
| Hourly (:05), every day | Auto-close sessions open longer than `MAX_SESSION_HOURS`: check-out is set so net time = the member's expected hours (never later than now). DM to the member, flag in `#attendance-admin`, audit log, custom status restored. |

Members are read from each team's Mattermost member list (bots and deactivated users excluded) and registered automatically, so people who never used `/att` still show as "not checked in". Each run is recorded in `job_runs`, so a restart never sends anything twice; a daily job missed during a restart still runs if the service is back within 2 hours.

**Custom status:** on check-in the bot sets 🟢 *Working* (🏠 *Working from home* for WFH), ☕ *On break* during breaks. Your own status is saved at check-in and restored at check-out (unless it has expired, or you changed your status yourself meanwhile). Status failures never block check-in/out.

**Buttons** are HMAC-signed (`HMAC_KEY`), only work for the member they were shown to, and expire after 48 hours.

## Development

```bash
go test ./...
go vet ./...
```

Unit tests cover the hours calculation, week/month ranges, custom status save/restore, button signing, config loading, slash-token verification, the admin check, auto-close math, job idempotency, reminders, workdays/holidays, the daily summary rules, team/user reports, expected hours with holidays, `/att fix` parsing, CSV export (incl. formula escaping) and the weekly report. The `/att fix` database transaction is covered by integration tests in phase 6. Integration tests against a real Postgres come in phase 6.

## Known limitations

- The system can't verify a member's physical location; WFH/office mode reflects approvals, not location.
- Leave isn't tracked yet: there is only a hook, so people on leave still get reminders and show as "not checked in".
- Mattermost doesn't sign requests to `/mm/*`. Slash commands are verified by per-team token; the service must stay reachable **only** on the internal Docker network — never publish its port.
