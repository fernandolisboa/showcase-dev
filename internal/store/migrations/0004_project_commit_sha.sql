-- The commit a Project was last published at (#19, build-at-publish). Nullable: a
-- Project is created as a draft with no build, so commit_sha is null until the first
-- successful publish, which resolves the repo's default-branch HEAD, builds every
-- service at that commit, and records the SHA here. It PINS the played version — the
-- play path builds exactly this commit (BuildSpec.Version) so a Guest gets the
-- published code and never waits on a rebuild from a moved HEAD. Stored as the UI
-- service's commit (the project headline); per-service commit capture for multi-repo
-- projects is a later slice.
ALTER TABLE projects ADD COLUMN commit_sha text;
