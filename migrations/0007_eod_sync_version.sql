-- Tasks + EOD, phase 6: sync_version counts saves of a report. A sync only
-- records its result for the version it claimed, so a resubmission during a
-- running sync is synced again instead of being marked done by the old one.
ALTER TABLE eod_reports ADD COLUMN sync_version INT NOT NULL DEFAULT 0;
