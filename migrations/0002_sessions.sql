-- +goose Up
-- Authentication sessions (PLATFORM_DECISIONS §B).
--
-- Sessions are global authentication state, not household-owned: one user may
-- hold sessions while belonging to several households, so this table is
-- deliberately exempt from the household_id rule (like users themselves).
--
-- Only the SHA-256 hex digest of an opaque 256-bit bearer token is stored. The
-- raw token is returned once at login and is never persisted; a database read
-- therefore cannot recover a usable credential. Sessions expire and can be
-- revoked individually (revoked_at) or all at once for a user.
--
-- ON DELETE CASCADE is intentional here: deleting a user identity must not
-- leave usable sessions behind. That differs from financial history, which uses
-- RESTRICT and is never deleted.

CREATE TABLE sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash text NOT NULL CHECK (length(btrim(token_hash)) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz NULL,
    CONSTRAINT sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT sessions_expiry_after_creation CHECK (expires_at > created_at),
    CONSTRAINT sessions_revocation_after_creation CHECK (
        revoked_at IS NULL OR revoked_at >= created_at
    )
);

-- The user-scoped index supports "revoke all sessions for this user" and
-- session administration. The expires_at index supports future cleanup of
-- expired rows; lookup by token uses the unique constraint above.
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- +goose Down
-- Test-teardown only. Dropping this table logs users out but destroys no
-- financial history.
DROP TABLE IF EXISTS sessions;
