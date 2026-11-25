#!/usr/bin/env bash
# Daily backup of the attendance database.
#
# Writes a compressed pg_dump (custom format) to $BACKUP_DIR, readable only
# by root, checks that it can be read back, and deletes backups older than
# $KEEP_DAYS days. Exits non-zero on any failure so cron reports it.
#
# Cron (see README):
#   30 2 * * * root /var/www/mattermost-attendance/scripts/backup.sh >> /var/log/attendance-backup.log 2>&1
set -euo pipefail
cd "$(dirname "$0")/.."

BACKUP_DIR="${BACKUP_DIR:-/var/backups/attendance}"
KEEP_DAYS="${KEEP_DAYS:-14}"
umask 077
mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"

stamp="$(date +%Y-%m-%d_%H%M)"
file="$BACKUP_DIR/attendance-$stamp.dump"
tmp="$file.partial"
trap 'rm -f "$tmp"' EXIT

log() { echo "$(date -Is) $*"; }

log "backup start → $file"
# </dev/null: exec reads stdin, which would swallow a script piped into bash.
docker compose exec -T attendance-db pg_dump -U attendance -d attendance -Fc -Z 6 < /dev/null > "$tmp"

# Verify: non-empty and pg_restore can read its table of contents.
if [ ! -s "$tmp" ]; then
  log "ERROR: dump is empty"; exit 1
fi
if ! docker compose exec -T attendance-db pg_restore --list < "$tmp" > /dev/null; then
  log "ERROR: dump is not readable by pg_restore"; exit 1
fi
mv "$tmp" "$file"
log "backup ok ($(du -h "$file" | cut -f1))"

# Retention: keep the last $KEEP_DAYS days.
deleted=$(find "$BACKUP_DIR" -maxdepth 1 -name 'attendance-*.dump' -type f -mtime +"$((KEEP_DAYS - 1))" -print -delete | wc -l)
log "retention: deleted $deleted backup(s) older than $KEEP_DAYS days; $(ls "$BACKUP_DIR"/attendance-*.dump | wc -l) kept"
