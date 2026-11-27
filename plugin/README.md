# Attendance plugin for Mattermost

The side panel for attendance, tasks and end-of-day reports (see [`../ui-plan.md`](../ui-plan.md) for the plan and progress). The plugin is a thin bridge: the panel (React/TypeScript) calls the plugin's server part (Go), which forwards to the attendance bot with the logged-in user's id and a shared secret. All rules live in the bot; `/att` commands keep working.

Started from the official [mattermost-plugin-starter-template](https://github.com/mattermost/mattermost-plugin-starter-template) (Apache 2.0, see `LICENSE`).

## Build

Needs Go and Node.js (see `.nvmrc`).

```bash
make dist            # → dist/com.bittrix.attendance-<version>.tar.gz
cd server && go test ./...
cd webapp && npx tsc --noEmit && npm test
```

The version is `version` in `plugin.json`.

## Settings (System Console → Plugins → Attendance)

| Setting | Value |
|---|---|
| Bot URL | `http://attendance:8080` (the bot on the internal Docker network) |
| Shared secret | the same value as `PLUGIN_SECRET` in the bot's `.env` (32+ characters) |

`GET /plugins/com.bittrix.attendance/api/v1/health` (logged in) reports whether the plugin is set up.

## Layout

| Path | What |
|---|---|
| `plugin.json` | id `com.bittrix.attendance`, version, settings |
| `server/` | Go: settings, HTTP bridge to the bot, live events |
| `webapp/src/` | React/TypeScript panel; `client.ts` calls the plugin's API (adds Mattermost's CSRF token) |
| `build/` | template build tools (manifest, pluginctl) |
