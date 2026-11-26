-- Tasks + EOD, phase 2: end-of-day reports (spec §9).
CREATE TABLE eod_reports (
  id             BIGSERIAL PRIMARY KEY,
  mm_user_id     TEXT NOT NULL REFERENCES members,
  team_id        TEXT NOT NULL,
  day            DATE NOT NULL,
  did_today      TEXT NOT NULL,
  blockers       TEXT,
  tomorrow       TEXT,
  gh_activity    JSONB,
  sync_status    TEXT NOT NULL DEFAULT 'pending' CHECK (sync_status IN ('pending','synced','failed')),
  sync_error     TEXT,
  reply_post_id  TEXT,
  submitted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (mm_user_id, team_id, day)
);
CREATE INDEX eod_reports_team_day ON eod_reports (team_id, day);

-- One row per issue in a report. new_status = old_status when unchanged;
-- synced = nothing left to write to GitHub (phase 3 writes the rest).
CREATE TABLE eod_items (
  report_id   BIGINT NOT NULL REFERENCES eod_reports ON DELETE CASCADE,
  issue_url   TEXT NOT NULL,
  issue_title TEXT,
  old_status  TEXT,
  new_status  TEXT NOT NULL,
  synced      BOOLEAN NOT NULL DEFAULT false,
  PRIMARY KEY (report_id, issue_url)
);

-- The daily "📝 EOD reports — <day>" root post per team.
CREATE TABLE eod_threads (
  team_id       TEXT NOT NULL,
  day           DATE NOT NULL,
  root_post_id  TEXT NOT NULL,
  PRIMARY KEY (team_id, day)
);
