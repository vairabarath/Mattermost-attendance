-- Initial schema (spec section 6), plus:
--   members.refreshed_at  when the profile was last fetched from Mattermost
--   job_runs              idempotency for scheduled jobs (section 9)
--   indexes for the common lookups

CREATE TABLE members (
  mm_user_id          TEXT PRIMARY KEY,
  username            TEXT NOT NULL,
  display_name        TEXT,
  expected_hours      NUMERIC NOT NULL DEFAULT 8,
  reminders_on        BOOLEAN NOT NULL DEFAULT true,
  tracked             BOOLEAN NOT NULL DEFAULT true,
  active              BOOLEAN NOT NULL DEFAULT true,
  saved_custom_status JSONB,
  refreshed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  id           BIGSERIAL PRIMARY KEY,
  mm_user_id   TEXT NOT NULL REFERENCES members,
  team_id      TEXT NOT NULL,
  check_in     TIMESTAMPTZ NOT NULL,
  check_out    TIMESTAMPTZ,
  work_mode    TEXT NOT NULL DEFAULT 'office' CHECK (work_mode IN ('office','wfh')),
  in_note      TEXT,
  out_summary  TEXT,
  auto_closed  BOOLEAN NOT NULL DEFAULT false,
  CHECK (check_out IS NULL OR check_out > check_in)
);
CREATE UNIQUE INDEX one_open_session ON sessions (mm_user_id) WHERE check_out IS NULL;
CREATE INDEX sessions_user_time ON sessions (mm_user_id, check_in);
CREATE INDEX sessions_team_time ON sessions (team_id, check_in);

CREATE TABLE breaks (
  id          BIGSERIAL PRIMARY KEY,
  session_id  BIGINT NOT NULL REFERENCES sessions ON DELETE CASCADE,
  start_at    TIMESTAMPTZ NOT NULL,
  end_at      TIMESTAMPTZ,
  CHECK (end_at IS NULL OR end_at >= start_at)
);
CREATE UNIQUE INDEX one_open_break ON breaks (session_id) WHERE end_at IS NULL;
CREATE INDEX breaks_session ON breaks (session_id);

CREATE TYPE wfh_status AS ENUM ('pending','approved','rejected','cancelled');
CREATE TABLE wfh_requests (
  id            BIGSERIAL PRIMARY KEY,
  mm_user_id    TEXT NOT NULL REFERENCES members,
  team_id       TEXT NOT NULL,
  start_date    DATE NOT NULL,
  end_date      DATE NOT NULL,
  half_day      BOOLEAN NOT NULL DEFAULT false,
  reason        TEXT NOT NULL,
  status        wfh_status NOT NULL DEFAULT 'pending',
  decided_by    TEXT,
  decided_at    TIMESTAMPTZ,
  decision_note TEXT,
  admin_post_id TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (end_date >= start_date)
);
CREATE INDEX wfh_user_team ON wfh_requests (mm_user_id, team_id, start_date);

CREATE TABLE holidays (
  day      DATE NOT NULL,
  team_id  TEXT,            -- NULL = applies to all teams
  name     TEXT NOT NULL
);
CREATE UNIQUE INDEX holidays_unique ON holidays (day, COALESCE(team_id, ''));

CREATE TABLE audit_log (
  id          BIGSERIAL PRIMARY KEY,
  actor_id    TEXT NOT NULL,
  target_id   TEXT NOT NULL,
  team_id     TEXT,
  action      TEXT NOT NULL,   -- fix, auto_close, wfh_approve, wfh_reject, ...
  old_value   JSONB,
  new_value   JSONB,
  reason      TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_target ON audit_log (target_id, created_at);

CREATE TABLE job_runs (
  job      TEXT NOT NULL,
  run_key  TEXT NOT NULL,      -- e.g. '2026-11-25' or '2026-11-25T14'
  ran_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (job, run_key)
);
