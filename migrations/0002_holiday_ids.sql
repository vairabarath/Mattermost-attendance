-- Phase 3: holidays get an id so `/att holiday remove <id>` can address them.
ALTER TABLE holidays ADD COLUMN id BIGSERIAL PRIMARY KEY;
