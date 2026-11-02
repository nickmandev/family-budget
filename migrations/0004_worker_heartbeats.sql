-- +goose Up
-- Operational telemetry for background processes, not household data; this
-- table intentionally has no household_id. Error summaries are truncated and
-- must never contain provider payloads, tokens, or account numbers.

CREATE TABLE worker_heartbeats (
    worker_name text PRIMARY KEY,
    started_at timestamptz NOT NULL,
    last_tick_at timestamptz NOT NULL,
    last_error_at timestamptz NULL,
    last_error_summary text NULL,
    CONSTRAINT worker_heartbeats_name_nonempty CHECK (length(btrim(worker_name)) > 0),
    CONSTRAINT worker_heartbeats_tick_after_start CHECK (last_tick_at >= started_at),
    CONSTRAINT worker_heartbeats_error_after_start CHECK (
        last_error_at IS NULL OR last_error_at >= started_at
    ),
    CONSTRAINT worker_heartbeats_error_summary_length CHECK (
        last_error_summary IS NULL OR length(last_error_summary) <= 500
    )
);

-- +goose Down
DROP TABLE IF EXISTS worker_heartbeats;
