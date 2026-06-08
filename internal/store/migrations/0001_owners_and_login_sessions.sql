-- Owners are the authenticated platform users. Auth is GitHub-only (ADR-0008):
-- github_user_id is GitHub's STABLE numeric id (it survives a handle rename),
-- so it is the identity key; github_login is the handle captured at last sign-in,
-- for display only. The public showcase username (ADR-0005) is decoupled from the
-- GitHub handle and is added, with the reserved-name guard, in a later #19 slice.
CREATE TABLE owners (
	id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	github_user_id bigint      NOT NULL UNIQUE,
	github_login   text        NOT NULL,
	created_at     timestamptz NOT NULL DEFAULT now(),
	updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Login sessions are the web sign-in sessions — distinct from the ephemeral demo
-- Sessions a Guest plays. Only a HASH of the opaque cookie token is stored, so a
-- database dump never yields usable live sessions. Rows are removed on logout and
-- pruned once past expires_at; ON DELETE CASCADE drops an Owner's sessions with
-- the Owner.
CREATE TABLE login_sessions (
	token_hash text        PRIMARY KEY,
	owner_id   bigint      NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL
);

CREATE INDEX login_sessions_owner_id_idx ON login_sessions (owner_id);
