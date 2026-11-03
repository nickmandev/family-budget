-- +goose Up
-- Household-scoped, one row per bank connection attempt. The state hash is
-- single-use and consumed on callback; only its SHA-256 hex digest is stored,
-- never the raw state. The provider session id is encrypted at rest and is an
-- account-access credential; it must never be logged. Remove rows never;
-- disconnect marks status.

CREATE TABLE bank_connections (
    id uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    aspsp_name text NOT NULL,
    aspsp_country char(2) NOT NULL CHECK (aspsp_country ~ '^[A-Z]{2}$'),
    status text NOT NULL CHECK (status IN ('PENDING', 'AUTHORIZED', 'FAILED', 'EXPIRED', 'DISCONNECTED')),
    state_hash text NOT NULL CHECK (length(btrim(state_hash)) > 0),
    state_expires_at timestamptz NOT NULL,
    initiated_by uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    authorization_id text NULL,
    provider_redirect_url text NULL,
    session_id_encrypted text NULL,
    session_expires_at timestamptz NULL,
    failure_summary text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT bank_connections_household_state_hash_key UNIQUE (household_id, state_hash),
    CONSTRAINT bank_connections_state_expiry_after_creation CHECK (state_expires_at > created_at),
    CONSTRAINT bank_connections_session_expiry_requires_session CHECK (
        session_expires_at IS NULL OR session_id_encrypted IS NOT NULL
    ),
    CONSTRAINT bank_connections_session_requires_authorized_status CHECK (
        session_id_encrypted IS NULL OR status IN ('AUTHORIZED', 'DISCONNECTED')
    )
);

CREATE INDEX bank_connections_household_status_idx ON bank_connections (household_id, status);
CREATE INDEX bank_connections_state_expires_at_idx ON bank_connections (state_expires_at);

-- +goose Down
-- Test-teardown only.
DROP TABLE IF EXISTS bank_connections;
