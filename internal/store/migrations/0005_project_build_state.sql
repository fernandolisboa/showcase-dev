-- The Owner-facing build lifecycle of a Project (#49, async publish). The existing
-- `published` boolean stays the play gate (a Guest may play only a published Project);
-- build_state is the richer lifecycle the Owner sees, so a build can be reported as in
-- progress or failed without ever flipping `published`. They are independent on purpose:
-- a Project that published successfully and is mid-rebuild is published=true (the old
-- build still plays) AND build_state='building'; if that rebuild fails it stays
-- published=true with build_state='failed' (a failed build never un-publishes).
--
--   idle      — created, or returned to draft by an edit; no build attempted since.
--   building  — a build is in progress (publish kicks it off; later it runs in the
--               background and the Owner polls this state).
--   failed    — the last build attempt failed; build_error holds a bounded log tail.
--   published — the last build succeeded and the Project is published.
--
-- build_started_at stamps when the current/last build began so a build stranded in
-- 'building' by a crash mid-build can be swept to 'failed' after a staleness threshold
-- (a later slice). build_error is null unless build_state = 'failed'.
ALTER TABLE projects
	ADD COLUMN build_state      text        NOT NULL DEFAULT 'idle',
	ADD COLUMN build_error      text,
	ADD COLUMN build_started_at timestamptz;

-- Existing published Projects predate build_state; reflect their successful build so a
-- pre-existing published row reads as 'published', not the 'idle' column default.
UPDATE projects SET build_state = 'published' WHERE published;
