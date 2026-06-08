-- The public showcase username (ADR-0005): the Portfolio URL segment
-- (showcase.dev/{username}), decoupled from the GitHub handle so a GitHub rename
-- never breaks a Portfolio URL (ADR-0008). Nullable — an Owner exists from first
-- sign-in but may not have claimed a username yet. Stored canonical (lowercased
-- by the app); UNIQUE gives case-insensitive uniqueness on that canonical form
-- (Postgres allows multiple NULLs, so un-claimed Owners don't collide). The
-- reserved-name guard (ADR-0005) is enforced in the app at claim time.
ALTER TABLE owners ADD COLUMN username text UNIQUE;
