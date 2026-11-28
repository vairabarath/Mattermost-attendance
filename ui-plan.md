# Attendance UI inside Mattermost — plan and progress

A side panel inside Mattermost for attendance, tasks and end-of-day reports, so people click instead of typing `/att` commands. Built as a **Mattermost plugin** that talks to the existing bot. Slash commands stay (mobile app, power users).

_Last updated: 28 Nov 2026 · Owner: barath · Status: **Phase 6 done — next: Phase 7 (admin: team today)**_

---

## Progress tracker

Legend: ⬜ not started · 🟦 in progress · ✅ done · ⏸️ waiting on a decision · ⛔ blocked

| Phase | What | Status | Deployed |
|---|---|---|---|
| **0** | Decisions and groundwork | ✅ | 27 Nov |
| **1** | Bot JSON API + plugin bridge (no UI yet) | ✅ live-tested | 27 Nov · bot `8a99918`, plugin 0.2.0 |
| **2** | Member panel: status, live timer, check in / break / check out | ✅ live-tested | 27 Nov · plugin 0.3.1 |
| **3** | Tasks in the panel: list, pick today's task, 🆕 badges | ✅ live-tested | 27 Nov · bot `c63a3e4`, plugin 0.4.0 |
| **4** | End-of-day report form in the panel | ✅ live-tested | 27 Nov · bot `1ea84b1`, plugin 0.5.1 |
| **5** | Live updates (panel refreshes itself) | ✅ live-tested | 27 Nov · bot `be0a410`, plugin 0.6.1 |
| **6** | Home + pages (navigation), Hours, Work from home, My settings | ✅ live-tested | 28 Nov · bot `bd9c6f2`, plugin 0.7.1 |
| **7** | Admin tab: team today, reports, task check | ⬜ | — |
| **8** | Weekly charts and polish (empty states, loading, errors, a11y) | ⬜ | — |
| **9** | Hardening, docs, release v1.0 | ⬜ | — |

Each phase: build → tests pass → your OK → push → deploy → live test → mark ✅ here.

---

## Decisions

| # | Question | Recommended | Status |
|---|---|---|---|
| D1 | Desktop app + browser first; phones keep slash commands (the mobile app doesn't show plugin panels)? | Yes | ✅ agreed (27 Nov) |
| D2 | Build the **member panel** first, then the admin tab? | Member panel first | ✅ agreed (27 Nov) |
| D3b | `mmctl plugin add` needs the upload switch: turn it on only during each install and back off (scripted) | Yes | ✅ agreed (27 Nov) |
| D3 | How to install our plugin: **A** keep plugin uploads on · **B** turn uploads on only while installing/upgrading, then off again (plugin keeps running) · **C** never on, install from the server terminal (`mmctl` local mode) | **C** | ✅ agreed (27 Nov): **C** — local mode was already on, so no setting change; installs and upgrades are part of Claude's deploy; the secret is copied server-side and never shown |
| D4 | Plugin id and name | `com.bittrix.attendance`, "Attendance" | proposed |
| D5 | Code location | `plugin/` folder in this repo | proposed |
| D6 | Keep slash commands | Yes, unchanged | ✅ agreed |

---

## Goals

- **One click** for the daily actions: check in, break, back, check out, pick a task, send the EOD report.
- **See at a glance:** am I checked in, for how long, which team, my tasks, what's left to do today.
- **Admins** see the team's day without typing commands.
- **Same rules everywhere:** the panel and `/att` call the same bot code, so they never disagree.
- **Nothing new on the internet:** no public ports, no extra login.

**Not in scope (v1):** a mobile UI; replacing the slash commands; changing attendance or GitHub rules; a separate website.

---

## Architecture

```
 Browser / Desktop app                    Mattermost server (Docker)                     Bot (Docker, internal only)
┌──────────────────────────┐   HTTPS    ┌──────────────────────────────────────┐  HTTP  ┌─────────────────────────────┐
│ Attendance panel (React) │──────────►│ /plugins/com.bittrix.attendance/api/*│──────►│ /api/v1/*  (JSON)           │
│  app-bar icon → RHS      │  (session  │  plugin server (Go):                 │ secret │  same code as /att commands │
│                          │   cookie)  │   • knows the user (Mattermost-      │ + user │  Postgres · GitHub App      │
│                          │◄──────────│     User-Id, set by Mattermost)      │   id   │                             │
│   live events (websocket)│            │   • forwards to the bot              │◄──────│  POST notify (user, event)  │
└──────────────────────────┘            │   • pushes live events to the user   │        └─────────────────────────────┘
                                        └──────────────────────────────────────┘
        /att slash commands and buttons keep working exactly as today ─────────────────────────► bot
```

**Request path:** panel → plugin server (Mattermost has already authenticated the user) → bot API with a **shared secret** and the **user id** → same service functions as the slash commands → JSON back.

**Live path:** something changes (panel action, slash command, job, GitHub sync) → bot calls the plugin's internal `notify` endpoint (shared secret) → plugin publishes a websocket event **to that user only** → panel refreshes the affected part.

### Stack

| Part | Choice |
|---|---|
| Panel | **React 18** (Mattermost's own copy), **TypeScript**, Redux via Mattermost's store, **plain CSS with Mattermost theme variables** (light/dark follow the user's theme), hand-written **SVG** for small charts |
| Build | Official **mattermost-plugin-starter-template**: webpack + Babel, `make dist` → `com.bittrix.attendance-x.y.z.tar.gz` |
| Plugin server | **Go**, Mattermost `server/public` plugin package (dependency of the plugin only; the bot stays stdlib + pgx + cron) |
| Bot API | Go standard library, inside the existing bot |
| Tests | Jest + React Testing Library (panel), Go tests (plugin server, bot API) |
| CI | GitHub Actions also builds and tests the plugin (Node 20+, Go) |
| Local tools | Node.js ✅ installed (v26), Go ✅ |

### Security model

- **Users:** the panel never sends a user id. The plugin server takes it from Mattermost's `Mattermost-User-Id` header, which Mattermost sets only for logged-in users. Requests without it are refused.
- **Plugin → bot:** header `X-Attendance-Secret` (a long random value, stored in the plugin's System Console settings and the bot's `.env`), compared in constant time. The bot's API accepts nothing else, and stays reachable only on the internal Docker network.
- **Bot → plugin (notify):** the same secret; the plugin accepts it only on its internal endpoint and only publishes to the user named in the event.
- **Team scope:** every request names a team; the bot checks the user is a member of it, and admin endpoints use the existing team-admin check.
- **Writes:** same rules as today — GitHub writes only on the member's own assigned issues, EOD window, idempotent check-in, etc. — because the API calls the same functions.
- **CSRF:** Mattermost requires its CSRF header on plugin requests from the web app; the panel uses Mattermost's client helpers, which add it.
- **No secrets in the browser.** No new public port.

---

## Bot API (v1)

All under `/api/v1`, JSON, team given as `?team=<team id>` or in the body. Errors: `{"error": "<message for people>"}` with a 4xx/5xx status.

| Method | Path | Who | Does |
|---|---|---|---|
| GET | `/me?team=` | member | My state: checked in?, since, on break, mode (🏢/🏠), today's hours (this team + all), teams I can use, admin?, GitHub linked? |
| POST | `/session/in` | member | Check in (optional note) |
| POST | `/session/break` · `/session/back` | member | Start / end a break |
| POST | `/session/out` | member | Check out (optional summary); returns whether an EOD report is due |
| GET | `/tasks?team=` | member | My open tasks (sorted, overdue/blocked flags, 🆕 off-board) |
| POST | `/tasks/pick` | member | Pick a task (`issue_url`) → In progress |
| GET | `/eod?team=&day=` | member | The EOD form data: relevant tasks, already-done, others, my saved report, sync status |
| POST | `/eod` | member | Submit / update the report (same validation as the dialog) |
| GET | `/hours?team=&range=today\|week\|month` | member | My hours per day and per org |
| GET · POST | `/wfh` · `/wfh/{id}/cancel` | member | My WFH requests / new request / cancel |
| GET | `/admin/today?team=&day=` | admin | Team table: in/out, hours, mode, EOD ✅/❌/⏳, Done, Blocked, sync |
| GET | `/admin/eod?team=&user=&day=` | admin | One member's report |
| GET | `/admin/tasks?team=&user=` | admin | One member's tasks |
| GET | `/admin/report?team=&range=week\|month` | admin | Team report rows (hours, expected, WFH, EOD rate, issues) |
| GET | `/admin/wfh/pending?team=` · POST `/admin/wfh/{id}/decide` | admin | Pending WFH, approve/reject |

Plugin internal: `POST /plugins/com.bittrix.attendance/internal/notify` `{user_id, event, team_id}` (secret required).

Live events (to one user): `session_changed`, `tasks_changed`, `eod_changed`, `wfh_changed`, and for admins `team_changed`.

---

## Screens

### Member panel (right-hand side, opened from the app-bar icon)

```
┌─ Attendance ─────────────── [Bittrix ▾] ┐
│ 🟢 Working · 3h 42m          🏢 Office   │   status card: live timer, mode
│ since 9:12 AM                            │
│ [ ☕ Break ]          [ 🔴 Check out ]   │   (not checked in: big [ ▶ Check in ] + note)
├──────────────────────────────────────────┤
│ Today's tasks                      (5)   │
│ ▶ #12 Pricing page        In progress    │   picked/active first
│ ⚠️ #9 Invoice — due 25 Nov     Todo [Pick]│   overdue flagged
│ 🚧 #7 Domain renewal        Blocked      │
│ ○ #15 Hiring post 🆕         Todo [Pick] │   🆕 just added to the board
│ [ show all · open board ↗ ]              │
├──────────────────────────────────────────┤
│ 📝 End-of-day report   ❌ not sent        │   ✅ sent · synced / ⏳ syncing / ⚠️ failed
│ [ Write report ]                         │
├──────────────────────────────────────────┤
│ Today 3h 42m · Week 21h 10m / 40h ▓▓▓▓░  │
│ 🏠 WFH: [ Request ]  · next: Mon 1 Dec ✅ │
└──────────────────────────────────────────┘
```

- **Team switcher** (only when the user is in several teams); defaults to the current Mattermost team.
- **Check in** with an optional note; mode comes from WFH approvals as today.
- **Check out** opens the EOD form right away (same as `/att out`).
- Errors show inline ("Couldn't load your tasks — retry"); attendance never waits on GitHub.

### End-of-day report (inside the panel, full height)

```
┌─ End of day · Fri 27 Nov ──────── [×] ┐
│ Tasks                                  │
│ #12 Pricing page                       │
│  (No change)(In progress)(In review)(Blocked)(Done)   ← chips, current one marked
│ #7 Domain renewal                      │
│  (No change)(In progress)(In review)(Blocked)(Done)
│ ✅ Already done today: #4, #5           │
│ Other tasks I finished  [ #15 ▾ ]      │
│ What I did today *   [              ]  │  (10+ characters)
│ Blockers             [              ]  │
│ Plan for tomorrow    [              ]  │
│              [ Cancel ]  [ Send report ]│
└────────────────────────────────────────┘
```
No 24-character label limit, full issue titles with links, the saved report pre-filled when editing, the 11:00 next-morning window enforced by the bot.

### Admin tab (admins of the current team)

```
┌─ Attendance ── [Me | Team] ─ [Bittrix ▾] ┐
│ Today · Fri 27 Nov        Sent 3 of 5    │
│ Member   Status      Hours  EOD  Done 🚧 │
│ @jana    🟢 9:05     6h 10m  ⏳   –   –  │
│ @ravi    🔴 out      8h 02m  ✅   2   1  │
│ @barath  ☕ break    3h 40m  ❌   –   –  │
│ ⚠️ Overdue: @jana #3 · 🐢 Stale: —       │
│ ⏳ WFH pending: 1  [Review]               │
│ [ Week report ] [ Month ] [ Export CSV ] │
└──────────────────────────────────────────┘
```
Click a member → their report and tasks. Approve or reject WFH requests inline.

---

## Phases and tasks

### Phase 0 — Decisions and groundwork 🟦

| # | Task | Who | Status |
|---|---|---|---|
| 0.1 | Decisions D1–D3 recorded | Claude | ✅ |
| 0.2 | Read-only check: how Mattermost's plugin settings are set (console or locked by env var) | Claude | ✅ plugins on · uploads off · signature not required · not locked by env · **local mode already on** (`mmctl --local` works) → option C possible with no setting change; to confirm with you at install time |
| 0.3 | `plugin/` from the official starter template; `plugin.json` (id, name, min server version, settings: Bot URL, Secret); builds with `make dist` | Claude | ✅ `com.bittrix.attendance` 0.1.0 builds (27 MB bundle; Linux amd64 + arm64); sample features removed; health endpoint (login required); Go + Jest tests pass |
| 0.4 | CI: build + test the plugin | Claude | ✅ both CI jobs green |
| 0.5 | Shared secret generated into the bot's `.env` on the VPS (never printed) | Claude | ✅ 64 chars, `.env` stays 600, backup taken |
| 0.6 | Install the plugin with `mmctl --local` | Claude | ✅ 0.2.0 enabled. `mmctl plugin add` needs plugin uploads on even in local mode, so (with your OK) the script turns them on only for the install and always back off — verified `false` afterwards |
| 0.7 | Shared secret + Bot URL into the plugin's settings with `mmctl --local config patch`, read from the bot's `.env` | Claude | ✅ never shown; GitHub/Jira/Boards settings verified unchanged; backup in `~/.mattermost-plugin-settings.*.json` |

### Phase 1 — Bot API + plugin bridge 🟦
- [x] Bot: `/api/v1` router, secret check (constant time), user id + configured team + Mattermost team membership checks, JSON errors (no internal details leak); off without `PLUGIN_SECRET`
- [x] Bot: `GET /me`, `POST /session/{in,break,back,out}` calling the same functions as `/att` (same messages, custom status, EOD)
- [x] Plugin server: proxy for `/api/v1/*` (user id from Mattermost's session, adds the secret, forwards no browser headers/cookies, resolves `..`, 64 KB / 2 MB limits, 15 s timeout, no redirects)
- [x] Plugin server: `internal/notify` (secret, known events, valid user) → websocket event to that one user
- [x] Bot: notifications after check-in, break, back, check-out and auto-close (background queue, never blocks)
- [x] Tests: bot API auth and errors, panel state/actions/notifications, notifier, config; plugin proxy, refusals and notify
- [x] Deploy tooling: `plugin/Dockerfile` (builds the bundle on the server), `scripts/deploy-plugin.sh` (install with `mmctl --local`), `scripts/plugin-config.sh` (secret + Bot URL into the plugin's settings; other plugins kept and checked; tested against a worst-case fake)
- [x] Deploy: secret into `.env`, bot, plugin (0.5–0.7); plugin uploads back to `false`
- [x] Live test (barath, Bittrix, desktop app session): health ✅ 0.2.0 · `/me` ✅ · unknown team → 400 ✅ · forged identity headers ignored ✅ · `..` escape → 404 ✅ · POST without CSRF token or XHR header → 401 ✅ · session cookie not readable by scripts ✅ · check in / check out through the plugin → same messages as `/att` ✅ · `session_changed` reached the browser's websocket within a second of each ✅ · no errors in the bot log ✅

### Phase 2 — Member panel: status and session ✅
- [x] App-bar icon (public/icon.svg) toggles the panel in the right-hand side
- [x] Panel follows the Mattermost team you're looking at (switch teams in Mattermost's sidebar); a non-attendance team (e.g. HQ) says so. *An in-panel team switcher isn't needed for now.*
- [x] Status card: Working / On break / Not checked in, live timer (ticks locally from the bot's `net_seconds`, re-syncs on every load), since-time, 🏢/🏠, break length; "in <other team>" when checked in elsewhere
- [x] Check in (optional note), Break / Back, Check out with a confirmation step; the bot's reply shown with Mattermost's own markdown formatter (sanitised), dismissible
- [x] Today here / all orgs (ticking)
- [x] Refresh: live events (`session_changed` etc.), reconnects, window becoming visible, every 5 min
- [x] Loading, error + retry, action errors; theme colours from Mattermost CSS variables (light/dark); focus outlines; aria labels
- [x] Bot: `/me` also returns seconds (`net_seconds`, `today_*_seconds`)
- [x] Tests: 10 Jest/RTL tests (states, timer, break, confirm, errors, live refresh); lint clean (also in CI)
- [x] Plugin 0.3.0; Linux amd64 only (the server is x86_64) → bundle 15 MB
- [x] Deploy: bot `bf004bb`, plugin 0.3.0 → 0.3.1; uploads back to `false` after each install
- [x] Live test (barath, Bittrix): icon in the app bar ✅ · panel opens, "Not checked in" ✅ · Check in from the panel → Working, timer ticks (0:00:01 → 0:00:04) ✅ · Break → paused (0:00:06 → 0:00:06), ▶ Back shown ✅ · Back ✅ · `/att out` typed in chat → open panel switched to "Not checked in" within 3 s ✅
- [x] Fixed after the live test: an old reply stayed under the new state after a change from chat → cleared on live refresh (0.3.1, live re-checked ✅)
- [x] Also fixed: a flaky bot test (`TestAPIAuth` depended on map order)

### Phase 3 — Tasks ✅
- [x] Bot: `GET /api/v1/tasks` (same list as `/att tasks`: sorted, overdue, off-board, picked today; off-board issues added to the board in the background) and `POST /api/v1/tasks/pick` (runs the chat pick menu's code: org check, assignment check on GitHub, records the pick)
- [x] Bot: a pick (panel or chat menu) sends `tasks_changed` so open panels refresh
- [x] Panel "Your tasks": status chip (Todo / In progress / In review / Blocked), title linking to GitHub (new tab), priority, due date (⚠️ overdue), 🆕 added to the board, ✓ Today for picked; Pick button per task with the bot's reply; 6 shown + "Show all N"
- [x] States: loading, not linked (ask an admin), GitHub down (Try again), no open tasks 🎉, hidden in teams without GitHub
- [x] Tests: bot (list states and flags, pick + notification, non-member, other org refused, endpoint input checks); panel (5 new: list marks, pick, show all, states + retry, pick error + live refresh); titles shown as text, never HTML
- [x] Plugin 0.4.0
- [x] Deploy: bot `c63a3e4`, plugin 0.4.0; uploads back to `false`
- [x] Live test (barath, Bittrix): panel lists #7 (Blocked, ⚠️ overdue, ✓ Today) and the new test issue #8 (Todo, Pick) ✅ · Pick on #8 → "now In progress", chip → In progress, ✓ Today ✅ · GitHub board #8 = In progress ✅ · `/att tasks` in chat shows the same list ✅

### Phase 4 — End-of-day form ✅
- [x] Bot: the dialog's form-building code split out (`eodFormData`, `eodState`) and shared — the panel shows exactly the dialog's rows; existing EOD tests unchanged and passing
- [x] Bot: `GET /api/v1/eod` (form for today / yesterday before 11:00; "nothing to report" and "closed" states; pre-filled from a saved report; sync line) and `POST /api/v1/eod` (verifies the **signed form state** like the dialog handler, then runs the dialog's `submitEOD`: same validation, saving, thread post, GitHub sync, blocker alerts)
- [x] Bot: `eod_changed` live event after a submission and after each GitHub sync
- [x] Panel: end-of-day card (Not sent yet → Write report / Sent · time · GitHub sync → Edit report / "Opens when you check out")
- [x] Panel: form view — full task titles (GitHub links), status buttons per task (No change / In progress / In review / Blocked / Done, coloured, keyboard and screen-reader friendly), already done today, other tasks as checkboxes, 3 text fields with counters, too-short check before sending, the bot's field errors next to each field, sticky Send / Update bar
- [x] Checking out in the panel opens the form (like the dialog after `/att out`); the form never reloads under you while open
- [x] Tests: bot (form = dialog rows, pre-fill, submit saves + posts + notifies, validation, other user's / team's form refused, signed state required, tampered state refused, unavailable / closed days); panel (21 tests: form parts, choices, sending, field errors, card states, check-out opens the form)
- [x] Plugin 0.5.0
- [x] Deploy: bot `1ea84b1`, plugin 0.5.0 → 0.5.1; uploads back to `false`
- [x] Live test (barath, Bittrix, today's report): card "Sent 8:06 AM · GitHub ✅ synced · Edit report" ✅ · form pre-filled (text, tasks #8 and #7 with "now:", already done #4 #5 #6) ✅ · #8 → Done + extra text → Update report ✅ · card "⏳ syncing…" → "✅ synced" by itself ~5 s later ✅ · GitHub #8 closed, board Done, EOD comment with the new text ✅ · thread reply edited in place (Done #4 #5 #6 #8) ✅ · no errors in the log ✅. Check-out → form not live-tested (you were checked in); covered by tests.
- [x] Fixed after the live test: the first status button showed the internal value `no_change` instead of "No change" (0.5.1; the tests now use the real value)

### Phase 5 — Live updates ✅
- [x] Already live from earlier phases: panel actions, `/att in/out/break/back`, chat buttons, auto-close, picks (panel + chat menu), EOD submit (panel + dialog), GitHub sync
- [x] New events: board sweep → `tasks_changed` to each linked assignee of an added issue (also when the run fails after adding); WFH submitted / approved / rejected / cancelled → `wfh_changed`; same-day WFH approval switching an open session to 🏠 → `session_changed`; an admin's `/att fix` → `session_changed` for the member
- [x] Targeted refresh: each event reloads only what it affects (status: session, wfh · tasks: tasks · end-of-day card: eod, session); reconnect, window shown again and the 5-min poll reload everything; bursts within 250 ms → one reload. A Break click no longer triggers two GitHub calls.
- [x] Events reach every window/device the user has open (Mattermost sends them per user); an open EOD form is never reloaded under you
- [x] Tests: bot (sweep notifies assignees, WFH approval events, `/att fix` event); panel (targeted refresh + debounce, disabled while the form is open, check-out elsewhere updates the status) — 23 panel tests
- [x] Plugin 0.6.0
- [x] Deploy: bot `ef3a7eb` → `be0a410`, plugin 0.6.0 → 0.6.1; uploads back to `false`
- [x] Live test with two Mattermost windows (barath): Break in A → B "On break" in 0.6 s ✅ · Back in B → A "Working" in 0.7 s ✅ · Break/Back now only call `/me` (4–5 ms), no GitHub-backed reloads ✅ · new issue assigned in `operations` (put on the board by GitHub's Auto-add) → appeared in both windows after 66 s, no reload ✅ · Pick in A → both windows reloaded tasks within 3 s; B shows it under "Picked for today" ✅
- [x] Fixed during the live test: (1) a break still reloaded the end-of-day card (GitHub-backed) → the card now reloads only on check-in/out (0.6.1); (2) issues put on the board by GitHub's own Auto-add, or newly assigned, sent no event → incremental sweeps now notify assignees of every issue created/changed since the last run (`be0a410`)
- Known: closing an issue on GitHub sends no event (the sweep only looks at open issues); the panel drops it at the next reload / 5-min poll

### Phase 6 — Home + pages, Hours, Work from home, My settings ✅

Your direction (28 Nov): instead of one long page of cards, a **home screen with tiles**; each tile opens its own page with **← Home**. Keep the greeting and the status bar (with the main action) on Home.

```
Home                                   Pages (each with ← Home)
┌ Good evening, Barath    BITTRIX ┐    ⏱ Attendance  timer, check in (note) / break / back / check out
│ ● Working · 1:58:12 [Check out] │    ✅ Tasks       picked for today, other open tasks, Pick
│ [⏱ Attendance ] [✅ Tasks      ] │    📝 End of day  status, write / edit report (form on the page)
│ [📝 End of day ] [🏠 WFH        ] │    🏠 WFH         + Request form, my requests ⏳✅❌, Cancel
│ [📊 Hours      ] [⚙️ My settings] │    📊 Hours       today / week / month, per day, vs expected, per org
│ (admins) [👥 Team today] (Ph. 7) │    ⚙️ Settings    GitHub link, my profile (view only), connection check
└──────────────────────────────────┘
```

- [x] Bot: `GET /hours` (today / week / month: per day with 🏢/🏠, this team, all orgs, per org, expected up to yesterday — the reports' calculation), `GET /wfh` + `POST /wfh` + `POST /wfh/{id}/cancel` (the `/att wfh` code: validation, overlap check, admin post with buttons, audit, live event), `GET /settings`; `/me` adds the GitHub login, display name, "GitHub set up" and the end-of-day summary for the Home tile (database only)
- [x] Fixed on the way: a WFH request without a chat channel (the panel) no longer tries to post a confirmation into chat; the month view's "only days with time" rule compared the wrong name
- [x] Panel: Home (greeting, status bar with the main action, 6 tiles with live summaries; Tasks tile only where GitHub is set up), pages with ← Home, the page remembered while Mattermost is open, checking out goes to the End of day page with the form open
- [x] Pages: Attendance (status card), Tasks, End of day (moved), Hours (tabs, progress against expected, per-day bars, by organization), Work from home (request form with field errors, list with status, cancel with confirmation), My settings (GitHub link, my settings view-only, connection check: plugin / attendance service / GitHub project)
- [x] Tests: bot (hours week/today/untracked, WFH request/overlap/past/list/cancel rules, settings, Home tile, endpoints); panel 32 tests (home, navigation and memory, status bar check in/out, attendance page, hours, WFH form and cancel, settings, live refresh)
- [x] Plugin 0.7.0
- [x] Deploy: bot `bd9c6f2`, plugin 0.7.0 → 0.7.1; uploads back to `false`
- [x] Live test (barath, Bittrix, Sat 28 Nov): Home — greeting, status bar "Working · 1:06:00 · Check out", 6 tiles with live summaries ✅ · Attendance page (timer, break, check out, today) ✅ · Tasks page (picked #7, overdue) ✅ · End of day page (today's report sent, synced, Edit) ✅ · Hours page (week: this org 12h 53m, all orgs 19h 10m, 41% of expected up to Fri, per-day bars with 🏢, today "● now", by organization) ✅ · My settings (GitHub ✅ vairabarath, settings, connection: plugin 0.7.1 / service / GitHub project ✅) ✅ · WFH: request Tue 1 Dec from the form → Pending, admin post in #attendance-admin; Cancel (confirmed) → Cancelled, admin post edited "🚫 Cancelled by member", buttons removed ✅ · no errors in the log ✅
- [x] Polished after the live test: the greeting capitalizes a lowercase name ("barath" → "Barath", 0.7.1)

### Phase 7 — Admin tab ⬜
- [ ] Bot: admin endpoints (team today, member report, member tasks, team report, WFH decide)
- [ ] Team today table with live status; member drill-down
- [ ] WFH approvals inline
- [ ] Week / month report table; CSV export (same as `/att export`)
- [ ] Admin-only (members never see the tab or reach the endpoints)
- [ ] Tests + live test as admin and as member

### Phase 8 — Charts and polish ⬜
- [ ] Week bar chart (hours per day vs expected), team EOD rate
- [ ] Keyboard access, focus order, screen-reader labels, colour contrast in both themes
- [ ] Empty states and help text; small-window layout
- [ ] Performance check (panel opens < 1 s; API p95 < 300 ms)

### Phase 9 — Hardening and release ⬜
- [ ] Security review (as in tasks phase 6): auth, team scope, secrets, injection, logs
- [ ] README: install / upgrade the plugin, settings, troubleshooting
- [ ] `docs/manual-test.md`: panel section
- [ ] Version 1.0.0 bundle; upgrade path tested (upload new version over old)

---

## Testing and rollout

- **Every phase:** Go tests (bot API, plugin server) + Jest/RTL (panel) + CI green → your OK → deploy → live test in the desktop app → tracker updated.
- **Deploy:** bot as today (`git pull`, `docker compose up -d --build attendance`); plugin (option C): build the bundle, copy it into the `mattermost` container, `mmctl --local plugin add --force` + `plugin enable` — no Mattermost restart, no setting change.
- **Rollback:** disable the plugin in System Console; slash commands keep working, nothing is lost (all data lives in the bot).

## Risks

| Risk | Mitigation |
|---|---|
| Mattermost upgrades change plugin APIs | Use only stable registry APIs; pin `min_server_version`; test after each Mattermost upgrade |
| Mobile users don't see the panel | Slash commands and buttons stay; documented |
| Panel and bot versions drift | `/api/v1` versioned; plugin shows "please update" if the bot is older |
| Secret leaks | Only in `.env` (bot) and System Console (plugin); rotate by changing both |
| More moving parts to deploy | One `make dist`, documented upload steps, CI builds the bundle |

## Changelog

- **28 Nov 2026** — Phase 6 deployed and live-tested: home screen with tiles and a page per tile (Attendance, Tasks, End of day, Hours, Work from home, My settings). Plugin 0.7.1.
- **27 Nov 2026** — Phase 5 deployed and live-tested: live updates across windows, targeted refresh, sweep notifications. Plugin 0.6.1.
- **27 Nov 2026** — Phase 4 deployed and live-tested: the end-of-day report in the panel (card + form, same code as the dialog). Plugin 0.5.1.
- **27 Nov 2026** — Phase 3 deployed and live-tested: "Your tasks" in the panel with Pick. Plugin 0.4.0.
- **27 Nov 2026** — Phase 2 deployed and live-tested: the Attendance panel (app-bar icon, live timer, check in / break / back / check out, live refresh). Plugin 0.3.1.
- **27 Nov 2026** — Phase 1 deployed and live-tested (bot API + plugin 0.2.0 bridge + live events). Install needed the upload switch briefly; scripted on → install → off.
- **27 Nov 2026** — Phase 0: decisions D1–D3 agreed (D3 = B); Mattermost checked (local mode already on); `plugin/` skeleton builds; CI job added.
- **27 Nov 2026** — Plan created. Waiting on D1–D3.
