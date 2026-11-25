# Mattermost Attendance Bot

Self-hosted attendance for Mattermost: check-in/out, breaks, hours, WFH approvals and reports — all through the `/att` slash command. Full spec: [attendance-bot-prompt.md](attendance-bot-prompt.md).

**Status: Phase 1 (core).** Available now: `/att in`, `/att out`, `/att hours [today]`, `/att help`.

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
     - Autocomplete on, hint `in | out | hours | help`
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

See [.env.example](.env.example). Required: `MM_URL`, `MM_BOT_TOKEN`, `POSTGRES_PASSWORD`, `TEAMS` and, for each team key, `TEAM_<KEY>_ID`, `TEAM_<KEY>_NAME`, `TEAM_<KEY>_SLASH_TOKEN`.

Teams not listed in `TEAMS` (e.g. HQ) get a friendly "attendance isn't used here" reply. A slash token is only accepted together with its own team's id.

## Commands (phase 1)

| Command | What it does |
|---|---|
| `/att in [note]` | Check in to this team. One open session across all teams. |
| `/att out [summary]` | Check out (from any team); shows this session and today's total. |
| `/att hours [today]` | Your hours today, per organization. |
| `/att help` | Command list. |

## Development

```bash
go test ./...
go vet ./...
```

Unit tests cover the hours calculation (`internal/attendance/hours_test.go`), config loading and slash-token verification. Integration tests against a real Postgres come in phase 6.

## Known limitations

- The system can't verify a member's physical location; WFH/office mode reflects approvals, not location.
- Mattermost doesn't sign requests to `/mm/*`. Slash commands are verified by per-team token; the service must stay reachable **only** on the internal Docker network — never publish its port.
