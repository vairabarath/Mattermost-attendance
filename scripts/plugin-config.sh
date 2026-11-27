#!/usr/bin/env bash
# Puts the bot's PLUGIN_SECRET (from .env) and the Bot URL into the Attendance
# plugin's settings in Mattermost, with mmctl local mode. Every other plugin's
# settings are sent back unchanged (and checked afterwards); a backup is kept
# in ~/.mattermost-plugin-settings.<time>.json (mode 600). Never prints the
# secret. Run on the server, from the repo root: scripts/plugin-config.sh
set -euo pipefail
MM=${MM_CONTAINER:-mattermost}
ID=com.bittrix.attendance
BOT_URL=${BOT_URL:-http://attendance:8080}
umask 077

SECRET=$(grep -E '^PLUGIN_SECRET=' .env | tail -1 | cut -d= -f2-)
if [ ${#SECRET} -lt 32 ]; then
  echo "PLUGIN_SECRET is missing or shorter than 32 characters in .env" >&2
  exit 1
fi

CUR=$(docker exec "$MM" mmctl --local config get PluginSettings.Plugins)
BACKUP="$HOME/.mattermost-plugin-settings.$(date +%Y%m%d%H%M%S).json"
printf '%s' "$CUR" > "$BACKUP"

PATCH=$(CUR="$CUR" SECRET="$SECRET" BOT_URL="$BOT_URL" ID="$ID" python3 -c '
import json, os
cur = json.loads(os.environ["CUR"]) or {}
mine = dict(cur.get(os.environ["ID"]) or {})
want = {"boturl": os.environ["BOT_URL"], "secret": os.environ["SECRET"]}
if all(mine.get(k) == v for k, v in want.items()):
    raise SystemExit
mine.update(want)
cur[os.environ["ID"]] = mine
print(json.dumps({"PluginSettings": {"Plugins": cur}}))
')
if [ -z "$PATCH" ]; then
  echo "plugin settings already set"
  exit 0
fi
printf '%s' "$PATCH" | docker exec -i "$MM" mmctl --local config patch /dev/stdin >/dev/null

AFTER=$(docker exec "$MM" mmctl --local config get PluginSettings.Plugins)
BEFORE="$CUR" AFTER="$AFTER" SECRET="$SECRET" BOT_URL="$BOT_URL" ID="$ID" python3 -c '
import json, os, sys
b, a, me = json.loads(os.environ["BEFORE"]) or {}, json.loads(os.environ["AFTER"]) or {}, os.environ["ID"]
others_ok = all(a.get(k) == v for k, v in b.items() if k != me)
mine_ok = (a.get(me) or {}).get("secret") == os.environ["SECRET"] and (a.get(me) or {}).get("boturl") == os.environ["BOT_URL"]
print("other plugins unchanged:", others_ok, "| attendance plugin set:", mine_ok)
sys.exit(0 if others_ok and mine_ok else 1)
' || { echo "CHECK FAILED — restore with: docker exec -i $MM mmctl --local config patch /dev/stdin <<< \"{\\\"PluginSettings\\\":{\\\"Plugins\\\":\$(cat $BACKUP)}}\"" >&2; exit 1; }
