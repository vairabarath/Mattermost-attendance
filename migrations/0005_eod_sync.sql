-- Tasks + EOD, phase 3: syncing reports to GitHub.
-- sync_attempts: tries since the last (re)submission; the retry job stops
-- after a maximum and alerts admins once.
-- sync_claimed_until: a short lease so the submit-time sync and the retry
-- job never write the same report to GitHub at the same time.
-- blocker_post_id: the report's alert in #attendance-admin (edited, not
-- reposted, when the report changes).
ALTER TABLE eod_reports
  ADD COLUMN sync_attempts      INT NOT NULL DEFAULT 0,
  ADD COLUMN sync_claimed_until TIMESTAMPTZ,
  ADD COLUMN blocker_post_id    TEXT;
CREATE INDEX eod_reports_sync ON eod_reports (sync_status, updated_at) WHERE sync_status <> 'synced';
