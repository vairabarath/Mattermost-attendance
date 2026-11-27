# Attendance UI inside Mattermost — plan and progress

A side panel inside Mattermost for attendance, tasks and end-of-day reports, so people click instead of typing `/att` commands. Built as a **Mattermost plugin** that talks to the existing bot. Slash commands stay (mobile app, power users).

_Last updated: 27 Nov 2026 · Owner: barath · Status: **Phase 0 in progress**_

---

## Progress tracker

Legend: ⬜ not started · 🟦 in progress · ✅ done · ⏸️ waiting on a decision · ⛔ blocked

| Phase | What | Status | Deployed |
|---|---|---|---|
| **0** | Decisions and groundwork | ✅ (install steps 0.5–0.7 run with Phase 1's deploy) | — |
| **1** | Bot JSON API + plugin bridge (no UI yet) | 🟦 built and tested — waiting for your OK to deploy | — |
| **2** | Member panel: status, live timer, check in / break / check out | ⬜ | — |
| **3** | Tasks in the panel: list, pick today's task, 🆕 badges | ⬜ | — |
| **4** | End-of-day report form in the panel | ⬜ | — |
| **5** | Live updates (panel refreshes itself) | ⬜ | — |
| **6** | Hours and WFH in the panel | ⬜ | — |
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
| 0.5 | Shared secret generated into the bot's `.env` on the VPS (never printed) | Claude | ⬜ (Phase 1) |
| 0.6 | Install the plugin with `mmctl --local` (option C; no setting change) | Claude | ⬜ (end of Phase 1) |
| 0.7 | Put the shared secret into the plugin's settings with `mmctl --local config set`, read from the bot's `.env` | Claude | ⬜ (end of Phase 1) |

### Phase 1 — Bot API + plugin bridge 🟦
- [x] Bot: `/api/v1` router, secret check (constant time), user id + configured team + Mattermost team membership checks, JSON errors (no internal details leak); off without `PLUGIN_SECRET`
- [x] Bot: `GET /me`, `POST /session/{in,break,back,out}` calling the same functions as `/att` (same messages, custom status, EOD)
- [x] Plugin server: proxy for `/api/v1/*` (user id from Mattermost's session, adds the secret, forwards no browser headers/cookies, resolves `..`, 64 KB / 2 MB limits, 15 s timeout, no redirects)
- [x] Plugin server: `internal/notify` (secret, known events, valid user) → websocket event to that one user
- [x] Bot: notifications after check-in, break, back, check-out and auto-close (background queue, never blocks)
- [x] Tests: bot API auth and errors, panel state/actions/notifications, notifier, config; plugin proxy, refusals and notify
- [x] Deploy tooling: `plugin/Dockerfile` (builds the bundle on the server), `scripts/deploy-plugin.sh` (install with `mmctl --local`), `scripts/plugin-config.sh` (secret + Bot URL into the plugin's settings; other plugins kept and checked; tested against a worst-case fake)
- [ ] Deploy: secret into `.env`, bot, plugin (0.5–0.7)
- [ ] Live test: `/api/v1/health` and `/api/v1/me` through the plugin from your logged-in browser; check in via the API → `/att status` agrees; notify reaches the browser

### Phase 2 — Member panel: status and session ⬜
- [ ] App-bar icon + right-hand panel; team switcher
- [ ] Status card with live timer (ticks locally, re-syncs from `/me`)
- [ ] Check in (note), Break / Back, Check out (→ EOD form)
- [ ] Loading, error and empty states; theme (light/dark) check
- [ ] Tests (Jest/RTL) for each state
- [ ] Live test with you in the desktop app

### Phase 3 — Tasks ⬜
- [ ] Bot: `GET /tasks`, `POST /tasks/pick`
- [ ] Task list: sort, ⚠️ overdue, 🚧 blocked, 🆕 off-board, links to GitHub, "show all"
- [ ] Pick button with inline result ("now In progress" / "left as is")
- [ ] Not linked to GitHub → friendly note
- [ ] Tests + live test

### Phase 4 — End-of-day form ⬜
- [ ] Bot: `GET /eod`, `POST /eod` (shared validation with the dialog)
- [ ] Form: status chips per task, already done, other tasks, text fields, edit existing report
- [ ] Sync status line (synced / syncing / failed) with live refresh
- [ ] Tests + live test (submit, edit, window closed after 11:00)

### Phase 5 — Live updates ⬜
- [ ] Events wired: slash command, buttons, jobs (auto-close), EOD sync, board sweep → panel refreshes
- [ ] Two open windows stay in step
- [ ] Tests + live test (check in by `/att in` → panel updates instantly)

### Phase 6 — Hours and WFH ⬜
- [ ] Bot: `GET /hours`, WFH endpoints
- [ ] Hours card: today, week vs expected, month; per-org split
- [ ] WFH: request form (dates, half day, reason), list with status, cancel
- [ ] Tests + live test

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

- **27 Nov 2026** — Phase 0: decisions D1–D3 agreed (D3 = B); Mattermost checked (local mode already on); `plugin/` skeleton builds; CI job added.
- **27 Nov 2026** — Plan created. Waiting on D1–D3.
