# Build: Mattermost Attendance Bot

You are building a self-hosted attendance system that runs entirely inside Mattermost through a bot. Read this whole spec before writing code. Work phase by phase (section 13), stop at the end of each phase so I can test, and ask me before making any decision this spec doesn't cover. Open questions are listed in section 16 — ask about them before the phase that needs them.

---

## 1. Context

- Self-hosted Mattermost **Team Edition** on my VPS (Docker), at `https://chat.bittrixtechnologies.com`, 15–20 members. Do not rely on any Enterprise/Professional-only feature.
- Five Mattermost teams exist:

  | Display name | Team name (URL slug) | Attendance? |
  |---|---|---|
  | HQ | `hq` | **No** — company-wide announcements only |
  | InkYank | `inkyank` | Yes |
  | Seechanlabs | `seechanlabs` | Yes |
  | Bittrix Technologies | `bittrixtechnologies` | Yes |
  | CreatorsCult | `creatorscult` | Yes |

- Some people belong to more than one organization team.
- **Administration:** there are 3 Mattermost **system admins** (`barath`, `janarthanan`, `madmap`). They are members of every team and manage all teams. There are currently no per-team admins, but the permission model must still support team admins (section 4) so it can be delegated later.
- **No dashboards or external tools** (no Grafana, no web UI). Everything — check-ins, reports, approvals — happens through the bot in Mattermost. The only export is CSV.
- Timezone for display and day boundaries: **Asia/Kolkata (IST)**. Store everything in UTC (`TIMESTAMPTZ`).

## 2. Features

1. Check in / check out, with optional notes.
2. Breaks (pause/resume), deducted from working hours.
3. Hours per day / week / month, with a per-organization breakdown.
4. Live "who's working" status, and the bot sets each member's Mattermost custom status.
5. Work-from-home requests: member requests, admins approve/reject with buttons, approved WFH changes the member's working mode for those dates.
6. Automatic handling of forgotten check-outs.
7. Daily and weekly summaries posted per team.
8. Admin corrections with a full audit log.
9. CSV export.

## 3. Tech stack

- **Go** (latest stable), single binary. Use the standard library `net/http` (or chi), `pgx` for Postgres, `robfig/cron/v3` for scheduling. Call the Mattermost REST API v4 with plain HTTP (or the official `mattermost/server/public/model` client if it stays lightweight).
- **PostgreSQL 16**, a separate database/container from Mattermost's own DB.
- **Docker Compose**, joining the existing external Docker network that Mattermost uses.
- SQL migrations as plain numbered `.sql` files (e.g. with `golang-migrate` or a tiny built-in runner).
- Config via environment variables (`.env`), never committed.

## 4. Mattermost integration

### Setup (document these steps in the README; I'll do them manually)
- Enable bot accounts; create bot `attendance`; generate its access token; give it the system admin role (required to set other users' custom status).
- System Console → Environment → Developer → *Allow untrusted internal connections to*: add the service hostname (`attendance`), since Mattermost calls it over the internal Docker network.
- In each of the **four organization teams** (not HQ):
  - Register slash command `/att` → `POST http://attendance:8080/mm/command`. Each team has its own verification token.
  - Create public channel `#attendance` (daily summaries).
  - Create private channel `#attendance-admin` (WFH approvals, auto-close flags, weekly reports). Add the 3 system admins.
  - Add the bot to the team and both channels.

### Team config
The service loads a mapping per team:
```
team_id → { name, slash_token, attendance_channel_id, admin_channel_id }
```
Load it from env/config file. Reject any slash command whose token doesn't match its `team_id`'s token. Ignore requests from teams not in the map (e.g. HQ) with a friendly message.

### Endpoints
- `POST /mm/command` — slash commands (form-encoded: `token, team_id, channel_id, user_id, user_name, command, text, trigger_id, response_url`).
- `POST /mm/action` — interactive button clicks (JSON payload with `user_id, post_id, channel_id, team_id, context`).
- `POST /mm/dialog` — interactive dialog submissions.
- `GET /healthz`.

Protect `/mm/action` and `/mm/dialog`: include a server-side HMAC-signed value in each button `context` / dialog `state` and verify it on receipt.

### Admin permission
No admin table — use Mattermost roles. A user is an **admin for team T** if either:
1. they are a **system admin**: `GET /api/v4/users/{user_id}` → `roles` contains `system_admin`; or
2. they are a **team admin** of T: `GET /api/v4/teams/{team_id}/members/{user_id}` → `scheme_admin == true` **or** `roles` contains `team_admin`.

Note: `system_admin` is a user-level role and never appears on the team membership, and current Mattermost versions represent team admins mainly via `scheme_admin`, so both checks and both fields are required.

Check on every admin command or approval click; cache results for ~60 seconds. Admin commands always act on the team the command was run in (or the team the button belongs to). A team admin can only act on their own team; system admins can act in any organization team by running the command there.

## 5. Commands

All replies are **ephemeral** unless noted. "Admin" means admin for this team as defined in section 4.

| Command | Who | Behaviour |
|---|---|---|
| `/att in [note]` | All | Start a session in this team. Reply includes buttons: ☕ Break, 🔴 Check out. |
| `/att out [summary]` | All | End the open session; reply with today's total. |
| `/att break` / `/att back` | All | Start/end a break on the open session. |
| `/att status` | All | Who is currently working / on break **in this team**, with since-time and mode (🏢/🏠). |
| `/att hours [today\|week\|month]` | All | Own hours, per-day table, total, and breakdown per organization. Default `today`. |
| `/att wfh` | All | Opens WFH request dialog. |
| `/att wfh list` | All | Own WFH requests and statuses. |
| `/att wfh cancel <id>` | All | Cancel own pending or future approved request. |
| `/att wfh pending` | Admin | Pending requests for this team. |
| `/att report @user [week\|month]` | Admin | That member's hours in this team. |
| `/att report team [week\|month]` | Admin | Markdown table for all members of this team. |
| `/att fix @user <YYYY-MM-DD> in HH:MM out HH:MM <reason>` | Admin | Replace that member's sessions in this team for that IST date (rules below); requires reason; writes audit log. |
| `/att export [YYYY-MM]` | Admin | CSV of this team's sessions for the month, uploaded as a file in a DM to the admin. Default current month. |
| `/att help` | All | Command list (show admin commands only to admins). |

Reports use Mattermost markdown tables. Format hours as `7h 45m`.

### `/att fix` rules
- Deletes **all** of the member's sessions in this team whose `check_in` falls on that IST date (their breaks cascade), and inserts **one** session with the given times, no breaks, `work_mode` from approved WFH for that date (same rule as check-in).
- `out` must be after `in` on the same date. (Overnight corrections are out of scope for now.)
- Refuses if the member has an **open** session in this team that started on that date — ask the member to check out first.
- Refuses future dates.
- The audit log row stores every deleted session (with breaks) as `old_value` and the new session as `new_value`.

## 6. Database schema (starting point — refine if needed, explain changes)

```sql
CREATE TABLE members (
  mm_user_id     TEXT PRIMARY KEY,
  username       TEXT NOT NULL,
  display_name   TEXT,
  expected_hours NUMERIC NOT NULL DEFAULT 8,   -- per day, across all teams combined
  reminders_on   BOOLEAN NOT NULL DEFAULT true,
  tracked        BOOLEAN NOT NULL DEFAULT true, -- false = excluded from reminders, "not checked in" lists and expected-hours comparisons
  active         BOOLEAN NOT NULL DEFAULT true,
  saved_custom_status JSONB,                  -- member's own custom status, saved before the bot overwrites it
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  id           BIGSERIAL PRIMARY KEY,
  mm_user_id   TEXT NOT NULL REFERENCES members,
  team_id      TEXT NOT NULL,
  check_in     TIMESTAMPTZ NOT NULL,
  check_out    TIMESTAMPTZ,
  work_mode    TEXT NOT NULL DEFAULT 'office' CHECK (work_mode IN ('office','wfh')),
  in_note      TEXT,
  out_summary  TEXT,
  auto_closed  BOOLEAN NOT NULL DEFAULT false,
  CHECK (check_out IS NULL OR check_out > check_in)
);
CREATE UNIQUE INDEX one_open_session ON sessions (mm_user_id) WHERE check_out IS NULL;

CREATE TABLE breaks (
  id          BIGSERIAL PRIMARY KEY,
  session_id  BIGINT NOT NULL REFERENCES sessions ON DELETE CASCADE,
  start_at    TIMESTAMPTZ NOT NULL,
  end_at      TIMESTAMPTZ
);
CREATE UNIQUE INDEX one_open_break ON breaks (session_id) WHERE end_at IS NULL;

CREATE TYPE wfh_status AS ENUM ('pending','approved','rejected','cancelled');
CREATE TABLE wfh_requests (
  id            BIGSERIAL PRIMARY KEY,
  mm_user_id    TEXT NOT NULL REFERENCES members,
  team_id       TEXT NOT NULL,
  start_date    DATE NOT NULL,
  end_date      DATE NOT NULL,
  half_day      BOOLEAN NOT NULL DEFAULT false,
  reason        TEXT NOT NULL,
  status        wfh_status NOT NULL DEFAULT 'pending',
  decided_by    TEXT,
  decided_at    TIMESTAMPTZ,
  decision_note TEXT,
  admin_post_id TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (end_date >= start_date)
);

CREATE TABLE holidays (
  day      DATE NOT NULL,
  team_id  TEXT,            -- NULL = applies to all teams
  name     TEXT NOT NULL
);

CREATE TABLE audit_log (
  id          BIGSERIAL PRIMARY KEY,
  actor_id    TEXT NOT NULL,
  target_id   TEXT NOT NULL,
  team_id     TEXT,
  action      TEXT NOT NULL,   -- fix, auto_close, wfh_approve, wfh_reject, ...
  old_value   JSONB,
  new_value   JSONB,
  reason      TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Create a SQL view (or a single Go function used everywhere) that computes **net worked time per member, per team, per IST calendar day** = session duration − break durations, splitting sessions (and breaks) that cross IST midnight. Sessions may legitimately run past midnight (late-night work), so this split is a real case, not an edge case. Every report and `/att hours` must use this one calculation so numbers always match.

Members are auto-registered on first `/att` use by fetching their profile from the Mattermost API; refresh `username`/`display_name` occasionally. Always key on `mm_user_id`, never username.

**Who counts as a member of a team** (for reminders, summaries, reports): users in the Mattermost team who are not bots (`is_bot`), not deactivated (`delete_at != 0`), and not `members.tracked = false`. Users who never used `/att` still count (they are simply "not checked in"); register them lazily from the team member list.

## 7. Business rules

- One open session per person **across all teams** (enforced by the unique index; handle the constraint error gracefully: "You're already checked in to InkYank since 9:42 AM").
- To switch organizations mid-day, the member checks out and checks in from the other team.
- `/att out` with an open break closes the break at check-out time.
- `/att break` without an open session, or a second break while on break → clear error.
- Multiple sessions per day are allowed and summed.
- **Work mode:** on `/att in`, if the member has an **approved** WFH request in this team covering today (IST), set `work_mode='wfh'`; otherwise `office`. Members cannot pick the mode themselves. If a same-day WFH request is approved while the member is already checked in to that team, update the open session to `wfh`.
- **Custom status:** on in → 🟢 "Working" (or 🏠 "Working from home"), on break → ☕ "On break", on out → restore.
  - Before the bot first overwrites it (on check-in), read the member's current custom status; if it isn't one the bot set, save it to `members.saved_custom_status`.
  - On check-out (manual or auto), restore the saved status if there is one (and it hasn't expired), otherwise clear. Then null the saved value.
  - Use `PUT /api/v4/users/{user_id}/status/custom` and `DELETE .../status/custom`. Failures here must never block the check-in itself; just log them.
- **Forgotten check-out:** see the auto-close job in section 9.
- All admin edits write to `audit_log` with old/new values and reason.

## 8. WFH request flow

1. `/att wfh` → open an interactive dialog via `POST /api/v4/actions/dialogs/open` using `trigger_id`. Fields: start date (text, `YYYY-MM-DD`), end date (text, optional, defaults to start), half day (bool), reason (textarea, required).
2. Validate on submit and return field errors in the dialog response: valid dates, end ≥ start, not in the past, no overlap with the member's pending/approved requests in this team. Optional per-team monthly WFH limit (config; show usage, don't block, unless configured to).
3. Save as `pending`. Bot posts in that team's `#attendance-admin`:
   `🏠 WFH request #12 — @priya — 28 Nov (full day) — Reason: … — WFH days used this month: 2` with **✅ Approve** / **❌ Reject** buttons. Mark as "⚡ urgent" if requested for the same day or after 8 PM IST for the next day. Store `admin_post_id`.
4. On click: verify HMAC context → verify clicker is an admin for that team (section 4) → in one transaction, `UPDATE … WHERE id=$1 AND status='pending'` (so two admins can't both decide). If 0 rows, reply ephemerally "Already decided".
   - Approve → update the post to `✅ Approved by @admin at 4:12 PM`, remove buttons.
   - Reject → open a small dialog asking for a reason, then update the post to `❌ Rejected by @admin — reason`.
5. DM the member with the outcome. Write audit log.
6. `/att wfh cancel` on a pending request updates the admin post to "Cancelled by member".

## 9. Scheduled jobs (IST, skip holidays and weekends — make workdays configurable)

"Members" below means tracked members as defined in section 6.

| Time | Job |
|---|---|
| 10:30 | DM members with `reminders_on` who belong to at least one attendance team and haven't checked in to **any** team today. One DM per person, not one per team. (Add a hook to skip people on leave later — leave is not in scope now, see section 16.) |
| 18:00 | In each `#attendance-admin`: list pending WFH requests for tomorrow. |
| 19:00 | DM anyone still checked in: "You're still checked in since …". |
| 20:00 | Per team, post daily summary to `#attendance` (details below). |
| Hourly | Auto-close forgotten sessions (details below). Runs every day, including weekends/holidays. |
| Mon 09:00 | Per team, post weekly report to `#attendance-admin` (details below). |

**Daily summary (20:00, per team):**
- Who worked in this team today, with net hours and 🏢/🏠 mode; 🏠 WFH count.
- Who is still checked in.
- "Worked for another org today": members of this team who checked in only to other teams (show which org) — they are **not** listed as absent.
- "Not checked in": members of this team who checked in to **no** attendance team today.

**Auto-close (hourly):**
- Close any open session that has been open longer than `MAX_SESSION_HOURS` (config, default 16).
- Set `check_out` so that **net** time equals the member's `expected_hours`: `check_out = check_in + expected_hours + total break time`, but never later than now. Close any open break at that `check_out`.
- Set `auto_closed=true`, restore custom status, DM the member, post a flag to the team's `#attendance-admin`, write audit log.
- Because this replaces the old fixed 23:59 close, sessions that cross midnight are allowed and are split by the hours calculation.

**Weekly report (Mon 09:00, per team, previous Mon–Sun):**
- Per member: hours in this team, hours across all orgs, expected hours (`expected_hours × workdays in the week`, minus holidays), WFH days in this team, auto-closed count.
- Expected hours are per person, not per team, so compare them with the **all-orgs** total; the per-team column is for information.

Jobs must be idempotent (safe if the service restarts and a job runs twice) — record job runs in a `job_runs` table keyed by job name + date (+ hour for the hourly job).

## 10. Security

- Verify slash tokens per team; verify HMAC on action/dialog payloads.
- Service listens only on the internal Docker network; no published ports.
- Secrets (bot token, slash tokens, HMAC key, DB password) only in `.env`, documented in `.env.example`.
- Admin checks always done server-side via the Mattermost API, never trusted from the request.
- Parameterized SQL only.
- Structured JSON logging; never log tokens.
- Known limitations (document in README):
  - The system can't verify a member's physical location; WFH/office mode reflects approvals, not location.
  - Mattermost does not sign `/mm/action` and `/mm/dialog` requests. The HMAC protects the `context`/`state` from tampering, but the `user_id` in those payloads is trusted. That is acceptable only because the service is reachable solely from the internal Docker network — never publish its port.

## 11. Deployment

- `Dockerfile` (multi-stage, distroless or alpine final image).
- `docker-compose.yml` with `attendance` and `attendance-db` services, joining the existing external network (name configurable, e.g. `mattermost`), named volume for Postgres data, `restart: unless-stopped`.
- Migrations run automatically on startup.
- `scripts/backup.sh`: `pg_dump` with date-stamped files and retention (keep 14 days); document a cron line for it.
- README: setup steps from section 4, env vars, how to add a holiday, how to exclude a user from tracking, how to run tests.

## 12. Project layout (suggested)

```
attendance/
  cmd/attendance/main.go
  internal/config/
  internal/mattermost/     # API client, payload types, signing
  internal/handlers/       # command, action, dialog routing
  internal/attendance/     # sessions, breaks, hours calculation
  internal/wfh/
  internal/reports/        # markdown tables, CSV
  internal/jobs/
  internal/store/          # pgx queries
  migrations/
  scripts/
  Dockerfile
  docker-compose.yml
  .env.example
  README.md
```

## 13. Build phases — stop after each one

1. **Core:** config, migrations, team mapping + token verification, auto member registration, `/att in`, `/att out`, `/att hours today`, `/att help`, Docker setup. **Write unit tests for the hours calculation first.**
2. **Daily use:** breaks, buttons on check-in reply, `/att status`, custom status updates (with save/restore), `/att hours week|month` with org breakdown.
3. **Jobs & summaries:** scheduler, job_runs idempotency, hourly auto-close, reminders, daily summary, holidays (`/att holiday`), member filtering (bots/deactivated/untracked), admin check (system admin + team admin, moved here from phase 4) and `/att member` settings.
4. **Admin:** `/att report`, `/att fix` + audit log, `/att export` CSV, weekly report.
5. **WFH:** dialogs, request flow, approve/reject buttons, work_mode on check-in, `/att wfh list|cancel|pending`, 18:00 pending reminder.
6. **Hardening:** integration tests against a real Postgres (testcontainers or compose), backup script, README, final review of security section.

## 14. Tests that must exist

Hours calculation:
- single session; multiple sessions in a day
- session crossing IST midnight is split across two days (including a break that crosses midnight)
- breaks deducted; open break closed at check-out
- auto-closed session: net time equals expected hours when breaks were taken; never later than now
- sessions in two teams the same day → correct per-team and total

Rules:
- double check-in rejected (including across teams)
- break without session / double break rejected
- WFH approval sets work_mode on check-in; non-approved stays office
- concurrent approve clicks → exactly one decision
- non-admin click on Approve rejected
- admin check: system admin allowed in any team; team admin via `scheme_admin` allowed; team admin of team A rejected in team B
- token from team A with team_id of team B rejected
- `/att fix` replaces all that day's sessions in that team only, refuses with an open session, writes old sessions to audit log
- custom status: member's own status saved on check-in and restored on check-out
- daily summary: member who worked only for another org is not listed as "not checked in"; bots and untracked users excluded

## 15. Working style

- Ask before adding dependencies beyond those listed.
- Keep functions small and code commented where logic is non-obvious (especially time math).
- After each phase: summarize what was built, list how I can test it manually in Mattermost, and note anything you deferred.

## 16. Open questions — ask me before the phase that needs them

1. **Are the 3 system admins tracked?** Should `barath`, `janarthanan`, `madmap` check in like everyone else, or default to `tracked=false`? (Phase 3)
   **Answer:** tracked like everyone else.
2. **How do we set `tracked`, `expected_hours` and `reminders_on` per member?** Options: an admin command (e.g. `/att member @user ...`), or env/config. (Phase 3)
   **Answer:** admin command `/att member @user ...`.
3. **Half-day WFH:** what does an approved half day change — mode for the whole day, or only informational? (Phase 5)
   **Answer:** informational only — the day stays 🏢; "½ WFH" is shown on the request and in lists. No monthly WFH limit for now.
4. **Leave:** is a simple leave mechanism wanted later (so people on leave aren't reminded or listed as absent)? For now only the hook exists. (Phase 3)
5. **Testcontainers:** OK to add `testcontainers-go` as a test dependency, or use the compose Postgres instead? (Phase 6)
