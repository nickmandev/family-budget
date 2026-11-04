-- +goose Up
-- Bank statement import history and suggestion provenance. Import rows remain
-- separate from the journal until a user confirms a suggestion.
CREATE TABLE bank_import_batches (
    id uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    account_id uuid NOT NULL,
    bank_code text NOT NULL,
    format text NOT NULL,
    file_name text NOT NULL,
    file_sha256 text NOT NULL,
    period_from date NULL,
    period_to date NULL,
    rows_total int NOT NULL,
    rows_imported int NOT NULL,
    rows_duplicate int NOT NULL,
    rows_failed int NOT NULL,
    created_by uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT bank_import_batches_household_id_key UNIQUE (household_id, id),
    CONSTRAINT bank_import_batches_account_fk FOREIGN KEY (household_id, account_id)
        REFERENCES accounts (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT bank_import_batches_rows_total_nonnegative CHECK (rows_total >= 0),
    CONSTRAINT bank_import_batches_rows_imported_nonnegative CHECK (rows_imported >= 0),
    CONSTRAINT bank_import_batches_rows_duplicate_nonnegative CHECK (rows_duplicate >= 0),
    CONSTRAINT bank_import_batches_rows_failed_nonnegative CHECK (rows_failed >= 0)
);

COMMENT ON TABLE bank_import_batches IS 'A persisted bank statement import attempt and its aggregate result counts.';

CREATE TABLE bank_import_entries (
    id uuid PRIMARY KEY,
    batch_id uuid NOT NULL REFERENCES bank_import_batches (id) ON DELETE RESTRICT,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    account_id uuid NOT NULL,
    fingerprint text NOT NULL,
    external_id text NOT NULL,
    effective_date date NOT NULL,
    value_date date NULL,
    direction text NOT NULL,
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    merchant text NOT NULL,
    description text NOT NULL,
    reference text NULL,
    transaction_code text NULL,
    status text NOT NULL DEFAULT 'BOOKED',
    suggestion_id uuid NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT bank_import_entries_account_fk FOREIGN KEY (household_id, account_id)
        REFERENCES accounts (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT bank_import_entries_batch_household_fk FOREIGN KEY (household_id, batch_id)
        REFERENCES bank_import_batches (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT bank_import_entries_direction_valid CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT bank_import_entries_amount_positive_bounded CHECK (
        amount_minor > 0 AND amount_minor <= 1000000000000000
    ),
    CONSTRAINT bank_import_entries_currency_length CHECK (length(currency) = 3),
    CONSTRAINT bank_import_entries_household_account_fingerprint_key
        UNIQUE (household_id, account_id, fingerprint)
);

COMMENT ON TABLE bank_import_entries IS 'Normalized statement rows retained for audit and idempotent re-import.';

CREATE INDEX bank_import_entries_batch_id_idx ON bank_import_entries (batch_id);

ALTER TABLE suggestions
    ADD COLUMN source_account_id uuid NULL,
    ADD COLUMN effective_date date NULL,
    ADD COLUMN direction text NULL,
    ADD COLUMN reference text NULL,
    ADD COLUMN raw_description text NULL,
    ADD COLUMN import_batch_id uuid NULL REFERENCES bank_import_batches (id) ON DELETE RESTRICT,
    ADD COLUMN fingerprint text NULL,
    ADD CONSTRAINT suggestions_import_direction_valid CHECK (
        direction IS NULL OR direction IN ('DEBIT', 'CREDIT')
    ),
    ADD CONSTRAINT suggestions_import_provenance_all_or_nothing CHECK (
        (source_account_id IS NULL AND effective_date IS NULL AND direction IS NULL AND fingerprint IS NULL AND import_batch_id IS NULL)
        OR
        (source_account_id IS NOT NULL AND effective_date IS NOT NULL AND direction IS NOT NULL AND fingerprint IS NOT NULL AND import_batch_id IS NOT NULL)
    ),
    ADD CONSTRAINT suggestions_source_account_fk FOREIGN KEY (household_id, source_account_id)
        REFERENCES accounts (household_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT suggestions_import_batch_household_fk FOREIGN KEY (household_id, import_batch_id)
        REFERENCES bank_import_batches (household_id, id) ON DELETE RESTRICT;

COMMENT ON COLUMN suggestions.source_account_id IS 'Account from which this imported suggestion originated; NULL for manual suggestions.';
COMMENT ON COLUMN suggestions.import_batch_id IS 'Import batch that produced this suggestion; NULL for manual suggestions.';
COMMENT ON COLUMN suggestions.fingerprint IS 'Stable source fingerprint used to suppress duplicate imported suggestions.';

-- +goose Down
ALTER TABLE suggestions
    DROP CONSTRAINT suggestions_import_batch_household_fk,
    DROP CONSTRAINT suggestions_source_account_fk,
    DROP CONSTRAINT suggestions_import_provenance_all_or_nothing,
    DROP CONSTRAINT suggestions_import_direction_valid,
    DROP COLUMN fingerprint,
    DROP COLUMN import_batch_id,
    DROP COLUMN raw_description,
    DROP COLUMN reference,
    DROP COLUMN direction,
    DROP COLUMN effective_date,
    DROP COLUMN source_account_id;
DROP TABLE IF EXISTS bank_import_entries;
DROP TABLE IF EXISTS bank_import_batches;
