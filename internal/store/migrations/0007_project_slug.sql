-- A human-friendly per-Owner URL slug for a Project (#51): nicer Portfolio/play URLs
-- showcase.dev/{username}/{slug} instead of the opaque UUID. Nullable and editable — a
-- Project may have no slug (the UUID still plays) and the Owner can change it later. UNIQUE
-- per Owner (the username namespaces it); Postgres allows multiple NULLs, so slugless
-- Projects don't collide. Stored canonical (lowercased + validated by the app at write
-- time, reserved-list-aware). The constraint is named so a slug collision is distinguishable
-- from the name collision (the app maps it to its own ErrProjectSlugTaken).
ALTER TABLE projects
	ADD COLUMN slug text,
	ADD CONSTRAINT projects_owner_slug_key UNIQUE (owner_id, slug);
