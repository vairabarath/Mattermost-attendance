-- Tasks + EOD, phase 1: link members to GitHub logins.
-- GitHub logins are case-insensitive, so uniqueness is on lower(login)
-- (the stored value keeps GitHub's canonical spelling).
ALTER TABLE members ADD COLUMN github_login TEXT;
CREATE UNIQUE INDEX members_github_login_key ON members (lower(github_login));
