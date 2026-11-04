-- +goose Up
-- Enable Banking was dropped before release; remove its unused table.
DROP TABLE IF EXISTS bank_connections;

-- +goose Down
-- Intentionally irreversible.
SELECT 1;
