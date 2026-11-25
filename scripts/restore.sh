#!/usr/bin/env bash
# Restore the attendance database from a backup made by backup.sh.
#
#   ./scripts/restore.sh /var/backups/attendance/attendance-2026-11-25_0230.dump --yes
#
# Steps: takes a safety backup of the current database, stops the bot,
# replaces the database contents with the backup, starts the bot again.
# Data recorded after the backup was taken is lost (except in the safety backup).
set -euo pipefail
cd "$(dirname "$0")/.."

file="${1:-}"
if [ -z "$file" ] || [ ! -f "$file" ]; then
  echo "usage: $0 <backup.dump> --yes" >&2; exit 2
fi
if [ "${2:-}" != "--yes" ]; then
  echo "This REPLACES the attendance database with $file." >&2
  echo "Re-run with --yes to continue." >&2
  exit 2
fi

echo "1/4 safety backup of the current database"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/attendance}/pre-restore" KEEP_DAYS=30 ./scripts/backup.sh

echo "2/4 stopping the bot"
docker compose stop attendance

echo "3/4 restoring $file"
docker compose exec -T attendance-db pg_restore -U attendance -d attendance --clean --if-exists --no-owner --single-transaction < "$file"

echo "4/4 starting the bot"
docker compose start attendance
sleep 10
docker compose ps
echo "restore done"
