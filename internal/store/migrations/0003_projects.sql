-- Projects are the Owner-configured apps a Portfolio lists and a Guest plays (#19).
-- A Project is an owner_id + a display name + the run contract: the Owner's
-- runcontract.Manifest (ADR-0003) stored verbatim as jsonb. The Manifest is a
-- self-contained, versioned document with its own Validate(), so keeping it as one
-- jsonb column — rather than shredding it into per-service/-env/-egress tables —
-- keeps the Go Manifest the single source of truth and a new manifest field a Go-only
-- change. The build columns (commit SHA, etc.) arrive with build-at-publish in a
-- later slice.
--
-- id is a uuid so the public play id is opaque and non-enumerable (a Guest plays by
-- this id; gen_random_uuid is core Postgres since 13, no extension). published gates
-- whether a Guest can play it: a Project is published only after a successful build
-- (flipped in the build slice), so a half-configured draft can never boot. ON DELETE
-- CASCADE drops an Owner's Projects with the Owner, matching login_sessions.
CREATE TABLE projects (
	id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
	owner_id   bigint      NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
	name       text        NOT NULL,
	manifest   jsonb       NOT NULL,
	published  boolean     NOT NULL DEFAULT false,
	created_at timestamptz NOT NULL DEFAULT now(),
	-- No trigger keeps this fresh (minimal-deps): any UPDATE must set updated_at =
	-- now() explicitly, as the owner.go mutations do.
	updated_at timestamptz NOT NULL DEFAULT now(),
	-- A Project name is the Owner-facing handle within their own Portfolio, so it is
	-- unique per Owner, not globally — two Owners may each have a "blog".
	UNIQUE (owner_id, name)
);

CREATE INDEX projects_owner_id_idx ON projects (owner_id);
