-- Tasks + EOD, phase 4: the issues a member picked as today's work (at
-- check-in or with /att tasks). Picked issues get a status dropdown in that
-- day's end-of-day form.
CREATE TABLE task_picks (
  mm_user_id  TEXT NOT NULL REFERENCES members,
  team_id     TEXT NOT NULL,
  day         DATE NOT NULL,
  issue_url   TEXT NOT NULL,
  picked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (mm_user_id, team_id, day, issue_url)
);
