-- +goose Up
-- Initial persistence schema for the household envelope-budgeting MVP.
--
-- This migration is the schema authority (STACK_DECISIONS §6): the GORM
-- adapters must not use AutoMigrate against deployed databases. It targets
-- PostgreSQL 18.x.
--
-- Money is always an integer number of minor units (BIGINT) plus the
-- household currency; no floating-point or NUMERIC money columns exist. Every
-- money constraint enforces the domain bound MaxMinorUnits = 10^15
-- (1_000_000_000_000_000) and the domain's non-zero/positive rules (Q8).
--
-- Household isolation is enforced by application authorization and reinforced
-- here: every household-owned table carries household_id, and composite
-- foreign keys on (household_id, <entity_id>) make it impossible to attach a
-- posting, reservation, plan link or idempotency record to another household's
-- row.
--
-- Public resource ids are UUID primary keys. Domain identifiers are
-- string-backed strong ids; the adapters convert between those strings and
-- UUIDs at the repository boundary. Internal append-only child rows
-- (postings, reservation changes, related links) use generated BIGINT identity
-- keys because they are not public resources and their insertion order is
-- useful for replay/diagnostics.
--
-- Envelopes intentionally have no `kind` column: the domain core exposes only
-- envelope identity, balance and reservation state (internal/domain/balance.go
-- has no EnvelopeKind type), so adding a kind here would create persistence
-- semantics the domain cannot validate. The linked card-payment account is
-- modelled because DOMAIN_MODEL §3.4 requires at most one payment envelope per
-- card liability; the application layer must still verify that the linked
-- account is a CREDIT_CARD_LIABILITY account.

CREATE TABLE households (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    currency_code char(3) NOT NULL CHECK (currency_code ~ '^[A-Z]{3}$'),
    currency_exponent smallint NOT NULL CHECK (currency_exponent BETWEEN 0 AND 6),
    timezone text NOT NULL CHECK (length(btrim(timezone)) > 0),
    ledger_version bigint NOT NULL DEFAULT 0 CHECK (ledger_version >= 0),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'ARCHIVED')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Referenced by journal_operations so an operation's recorded currency can
    -- never disagree with its household. The default NO ACTION update rule
    -- also prevents silently changing the household currency while financial
    -- history exists (DOMAIN_MODEL §3.1).
    CONSTRAINT households_id_currency_key UNIQUE (id, currency_code, currency_exponent)
);

CREATE TABLE users (
    id uuid PRIMARY KEY,
    email text NOT NULL CHECK (length(btrim(email)) > 0),
    password_hash text NOT NULL CHECK (length(password_hash) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Case-insensitive uniqueness without requiring the citext extension. The
-- application normalizes email for lookup with the same lower() expression.
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

CREATE TABLE household_memberships (
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    role text NOT NULL CHECK (role IN ('OWNER', 'MEMBER')),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'REMOVED')),
    joined_at timestamptz NOT NULL DEFAULT now(),
    removed_at timestamptz NULL,
    PRIMARY KEY (household_id, user_id),
    CONSTRAINT household_memberships_removal_consistent CHECK (
        (status = 'ACTIVE' AND removed_at IS NULL)
        OR (status = 'REMOVED' AND removed_at IS NOT NULL)
    )
);

CREATE INDEX household_memberships_user_id_idx ON household_memberships (user_id);

CREATE TABLE accounts (
    id uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    kind text NOT NULL CHECK (kind IN ('CHECKING', 'SAVINGS', 'CASH_WALLET', 'CREDIT_CARD_LIABILITY')),
    budget_eligible boolean NOT NULL,
    status text NOT NULL CHECK (status IN ('ACTIVE', 'ARCHIVED')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT accounts_household_id_id_key UNIQUE (household_id, id),
    -- Mirrors domain.NewAccountMeta: a card liability is never budgetable cash.
    CONSTRAINT accounts_card_liability_not_eligible CHECK (
        kind <> 'CREDIT_CARD_LIABILITY' OR budget_eligible = false
    )
);

CREATE INDEX accounts_household_status_idx ON accounts (household_id, status);
CREATE INDEX accounts_household_kind_idx ON accounts (household_id, kind);

CREATE TABLE envelopes (
    id uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    status text NOT NULL CHECK (status IN ('ACTIVE', 'ARCHIVED')),
    linked_card_account_id uuid NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT envelopes_household_id_id_key UNIQUE (household_id, id),
    -- Nullable target: MATCH SIMPLE skips the check while no card is linked.
    CONSTRAINT envelopes_linked_card_fk FOREIGN KEY (household_id, linked_card_account_id)
        REFERENCES accounts (household_id, id) ON DELETE RESTRICT
);

CREATE INDEX envelopes_household_status_idx ON envelopes (household_id, status);

-- DOMAIN_MODEL §3.4: one dedicated card-payment envelope per card liability
-- for the MVP. Archived envelopes are excluded so a replacement can be created.
CREATE UNIQUE INDEX envelopes_active_linked_card_key
    ON envelopes (household_id, linked_card_account_id)
    WHERE linked_card_account_id IS NOT NULL AND status = 'ACTIVE';

-- Planned operations live in their own table because the lifecycle aggregate
-- is loaded and saved independently of the ledger. The released/final amount
-- columns are present because the domain aggregate records them after
-- completion/cancellation; persisting the whole aggregate keeps a reloaded plan
-- faithful to its in-memory state.
CREATE TABLE plans (
    id uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    kind text NOT NULL CHECK (kind IN ('PURCHASE', 'REPAYMENT')),
    state text NOT NULL CHECK (state IN ('DRAFT', 'RESERVED', 'RESERVED_EXCEPTION', 'COMPLETED', 'CANCELLED')),
    target_envelope_id uuid NOT NULL,
    target_account_id uuid NULL,
    intended_amount_minor bigint NOT NULL,
    authorized_amount_minor bigint NULL,
    mode text NULL CHECK (mode IN ('NORMAL', 'EMERGENCY_OVERRIDE')),
    reason text NULL CHECK (reason IS NULL OR char_length(reason) <= 500),
    held_amount_minor bigint NOT NULL DEFAULT 0,
    held_on_account_minor bigint NOT NULL DEFAULT 0,
    released_amount_minor bigint NOT NULL DEFAULT 0,
    released_on_account_minor bigint NOT NULL DEFAULT 0,
    final_amount_minor bigint NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT plans_household_id_id_key UNIQUE (household_id, id),
    CONSTRAINT plans_target_envelope_fk FOREIGN KEY (household_id, target_envelope_id)
        REFERENCES envelopes (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT plans_target_account_fk FOREIGN KEY (household_id, target_account_id)
        REFERENCES accounts (household_id, id) ON DELETE RESTRICT,
    -- Q2: a planned repayment always holds a source cash account; a purchase
    -- may omit it (card purchase, Q1).
    CONSTRAINT plans_repayment_requires_account CHECK (
        kind <> 'REPAYMENT' OR target_account_id IS NOT NULL
    ),
    CONSTRAINT plans_intended_amount_positive_bounded CHECK (
        intended_amount_minor > 0 AND intended_amount_minor <= 1000000000000000
    ),
    CONSTRAINT plans_authorized_amount_positive_bounded CHECK (
        authorized_amount_minor IS NULL
        OR (authorized_amount_minor > 0 AND authorized_amount_minor <= 1000000000000000)
    ),
    CONSTRAINT plans_held_amount_nonnegative_bounded CHECK (
        held_amount_minor >= 0 AND held_amount_minor <= 1000000000000000
    ),
    CONSTRAINT plans_held_on_account_nonnegative_bounded CHECK (
        held_on_account_minor >= 0 AND held_on_account_minor <= 1000000000000000
    ),
    CONSTRAINT plans_released_amount_nonnegative_bounded CHECK (
        released_amount_minor >= 0 AND released_amount_minor <= 1000000000000000
    ),
    CONSTRAINT plans_released_on_account_nonnegative_bounded CHECK (
        released_on_account_minor >= 0 AND released_on_account_minor <= 1000000000000000
    ),
    CONSTRAINT plans_final_amount_positive_bounded CHECK (
        final_amount_minor IS NULL
        OR (final_amount_minor > 0 AND final_amount_minor <= 1000000000000000)
    ),
    -- A target without an account can never report an account hold. This is
    -- the Q1 card-purchase shape.
    CONSTRAINT plans_no_account_hold_without_account CHECK (
        target_account_id IS NOT NULL OR held_on_account_minor = 0
    ),
    CONSTRAINT plans_no_account_release_without_account CHECK (
        target_account_id IS NOT NULL OR released_on_account_minor = 0
    ),
    CONSTRAINT plans_completed_has_final_amount CHECK (
        state <> 'COMPLETED' OR final_amount_minor IS NOT NULL
    ),
    -- Mirrors domain.PlannedOperation: an override must have a reason, a
    -- normal authorization has none, and only a draft has no authorization
    -- mode. Completed/cancelled plans keep the mode they had when reserved.
    CONSTRAINT plans_state_mode_consistent CHECK (
        (state = 'DRAFT' AND mode IS NULL AND reason IS NULL)
        OR (state = 'RESERVED' AND mode = 'NORMAL' AND reason IS NULL)
        OR (state = 'RESERVED_EXCEPTION' AND mode = 'EMERGENCY_OVERRIDE' AND reason IS NOT NULL)
        OR (
            state IN ('COMPLETED', 'CANCELLED')
            AND (
                (mode IS NULL AND reason IS NULL)
                OR (mode = 'NORMAL' AND reason IS NULL)
                OR (mode = 'EMERGENCY_OVERRIDE' AND reason IS NOT NULL)
            )
        )
    )
);

CREATE INDEX plans_household_state_idx ON plans (household_id, state);
CREATE INDEX plans_household_target_envelope_idx ON plans (household_id, target_envelope_id);
CREATE INDEX plans_household_target_account_idx ON plans (household_id, target_account_id);

CREATE TABLE suggestions (
    id uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    source text NOT NULL CHECK (source IN (
        'MANUAL', 'PLANNED_PURCHASE', 'EXTERNAL_ENTRY', 'OPENING_BALANCE', 'RECONCILIATION'
    )),
    amount_minor bigint NOT NULL,
    merchant text NOT NULL,
    state text NOT NULL CHECK (state IN ('SUGGESTED', 'EDITED', 'CONFIRMED', 'CANCELLED')),
    cancel_reason text NULL,
    confirmation_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT suggestions_household_id_id_key UNIQUE (household_id, id),
    CONSTRAINT suggestions_amount_positive_bounded CHECK (
        amount_minor > 0 AND amount_minor <= 1000000000000000
    ),
    CONSTRAINT suggestions_confirmation_count_nonnegative CHECK (confirmation_count >= 0),
    CONSTRAINT suggestions_cancel_reason_only_when_cancelled CHECK (
        state = 'CANCELLED' OR cancel_reason IS NULL
    ),
    CONSTRAINT suggestions_confirmed_has_count CHECK (
        state <> 'CONFIRMED' OR confirmation_count >= 1
    )
);

CREATE INDEX suggestions_household_state_idx ON suggestions (household_id, state);

-- The append-only journal. There is deliberately no updated_at column and no
-- UPDATE/DELETE path in the schema design. The unique key on
-- (household_id, idempotency_key) is the database-level idempotency guarantee.
-- actor_id references users; a documented system actor is a seeded users row
-- with a fixed UUID and an unusable password hash.
CREATE TABLE journal_operations (
    id uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    actor_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    op_type text NOT NULL CHECK (op_type IN (
        'OPENING_BALANCE', 'INCOME', 'REIMBURSEMENT', 'ALLOCATE', 'REALLOCATE',
        'DEBIT_PURCHASE', 'CASH_PURCHASE', 'CARD_PURCHASE', 'CARD_PAYMENT',
        'CARD_FEE_INTEREST', 'TRANSFER_ELIGIBLE', 'TRANSFER_TO_ELIGIBLE',
        'TRANSFER_OUT_OF_SCOPE', 'TRANSFER_EXCLUDED', 'CASH_REFUND', 'CARD_REFUND',
        'RESERVATION_CREATE', 'RESERVATION_RESIZE', 'RESERVATION_CANCEL',
        'PURCHASE_COMPLETE', 'EXTERNAL_TRANSACTION', 'REVERSAL', 'REPLACEMENT',
        'CATEGORY_CORRECTION', 'ACCOUNT_CORRECTION', 'METADATA_CORRECTION',
        'RECONCILE_INCREASE', 'RECONCILE_DECREASE'
    )),
    source text NOT NULL CHECK (source IN (
        'MANUAL', 'PLANNED_PURCHASE', 'EXTERNAL_ENTRY', 'OPENING_BALANCE', 'RECONCILIATION'
    )),
    effective_date date NOT NULL,
    currency_code char(3) NOT NULL CHECK (currency_code ~ '^[A-Z]{3}$'),
    currency_exponent smallint NOT NULL CHECK (currency_exponent BETWEEN 0 AND 6),
    amount_minor bigint NULL,
    reason text NULL CHECK (reason IS NULL OR char_length(reason) <= 500),
    idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) > 0),
    request_fingerprint text NOT NULL CHECK (length(btrim(request_fingerprint)) > 0),
    created_at timestamptz NOT NULL,
    committed_at timestamptz NOT NULL,
    CONSTRAINT journal_operations_household_id_id_key UNIQUE (household_id, id),
    CONSTRAINT journal_operations_idempotency_key_key UNIQUE (household_id, idempotency_key),
    CONSTRAINT journal_operations_committed_after_created CHECK (committed_at >= created_at),
    CONSTRAINT journal_operations_amount_positive_bounded CHECK (
        amount_minor IS NULL
        OR (amount_minor > 0 AND amount_minor <= 1000000000000000)
    ),
    CONSTRAINT journal_operations_household_currency_fk
        FOREIGN KEY (household_id, currency_code, currency_exponent)
        REFERENCES households (id, currency_code, currency_exponent) ON DELETE RESTRICT
);

CREATE INDEX journal_operations_household_date_idx
    ON journal_operations (household_id, effective_date, committed_at, id);
CREATE INDEX journal_operations_household_committed_idx
    ON journal_operations (household_id, committed_at, id);
CREATE INDEX journal_operations_household_type_idx
    ON journal_operations (household_id, op_type);

-- Typed posting rows per Q9. household_id is denormalized so isolation filters
-- do not need a join, and the composite foreign keys guarantee it always
-- matches both the operation and the account/envelope.
CREATE TABLE account_postings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    household_id uuid NOT NULL,
    journal_operation_id uuid NOT NULL,
    account_id uuid NOT NULL,
    delta_minor bigint NOT NULL,
    CONSTRAINT account_postings_delta_nonzero_bounded CHECK (
        delta_minor <> 0
        AND delta_minor BETWEEN -1000000000000000 AND 1000000000000000
    ),
    CONSTRAINT account_postings_journal_operation_fk
        FOREIGN KEY (household_id, journal_operation_id)
        REFERENCES journal_operations (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT account_postings_account_fk
        FOREIGN KEY (household_id, account_id)
        REFERENCES accounts (household_id, id) ON DELETE RESTRICT
);

CREATE INDEX account_postings_account_idx ON account_postings (account_id);
CREATE INDEX account_postings_journal_operation_idx ON account_postings (journal_operation_id);
CREATE INDEX account_postings_household_account_idx ON account_postings (household_id, account_id);

CREATE TABLE envelope_postings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    household_id uuid NOT NULL,
    journal_operation_id uuid NOT NULL,
    envelope_id uuid NOT NULL,
    delta_minor bigint NOT NULL,
    CONSTRAINT envelope_postings_delta_nonzero_bounded CHECK (
        delta_minor <> 0
        AND delta_minor BETWEEN -1000000000000000 AND 1000000000000000
    ),
    CONSTRAINT envelope_postings_journal_operation_fk
        FOREIGN KEY (household_id, journal_operation_id)
        REFERENCES journal_operations (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT envelope_postings_envelope_fk
        FOREIGN KEY (household_id, envelope_id)
        REFERENCES envelopes (household_id, id) ON DELETE RESTRICT
);

CREATE INDEX envelope_postings_envelope_idx ON envelope_postings (envelope_id);
CREATE INDEX envelope_postings_journal_operation_idx ON envelope_postings (journal_operation_id);
CREATE INDEX envelope_postings_household_envelope_idx ON envelope_postings (household_id, envelope_id);

-- Reservation hold transitions. The domain stores an envelope delta on every
-- change and an optional, separately-valued account delta (reservation.go:
-- EnvelopeHold plus optional AccountHold). The schema keeps those two deltas
-- distinct rather than collapsing them into one column, so a row can faithfully
-- represent e.g. a completion that releases a differently-sized account hold.
CREATE TABLE reservation_changes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    household_id uuid NOT NULL,
    journal_operation_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    envelope_id uuid NOT NULL,
    account_id uuid NULL,
    envelope_delta_minor bigint NOT NULL,
    account_delta_minor bigint NULL,
    CONSTRAINT reservation_changes_envelope_delta_nonzero_bounded CHECK (
        envelope_delta_minor <> 0
        AND envelope_delta_minor BETWEEN -1000000000000000 AND 1000000000000000
    ),
    CONSTRAINT reservation_changes_account_delta_shape CHECK (
        (account_id IS NULL AND account_delta_minor IS NULL)
        OR (
            account_id IS NOT NULL
            AND account_delta_minor IS NOT NULL
            AND account_delta_minor <> 0
            AND account_delta_minor BETWEEN -1000000000000000 AND 1000000000000000
        )
    ),
    CONSTRAINT reservation_changes_journal_operation_fk
        FOREIGN KEY (household_id, journal_operation_id)
        REFERENCES journal_operations (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT reservation_changes_plan_fk
        FOREIGN KEY (household_id, plan_id)
        REFERENCES plans (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT reservation_changes_envelope_fk
        FOREIGN KEY (household_id, envelope_id)
        REFERENCES envelopes (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT reservation_changes_account_fk
        FOREIGN KEY (household_id, account_id)
        REFERENCES accounts (household_id, id) ON DELETE RESTRICT
);

CREATE INDEX reservation_changes_plan_idx ON reservation_changes (plan_id);
CREATE INDEX reservation_changes_journal_operation_idx ON reservation_changes (journal_operation_id);
CREATE INDEX reservation_changes_envelope_idx ON reservation_changes (envelope_id);
CREATE INDEX reservation_changes_account_idx
    ON reservation_changes (account_id) WHERE account_id IS NOT NULL;

-- Typed related-operation links. The domain's RelatedOperationRef always
-- targets a journal operation; target_plan_id is reserved for plan-origin
-- links and exactly one of the two targets must be present.
CREATE TABLE related_links (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    household_id uuid NOT NULL,
    journal_operation_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('ORIGINAL', 'REVERSAL', 'REFUND', 'REPLACEMENT', 'PLAN')),
    target_operation_id uuid NULL,
    target_plan_id uuid NULL,
    CONSTRAINT related_links_exactly_one_target CHECK (
        num_nonnulls(target_operation_id, target_plan_id) = 1
    ),
    CONSTRAINT related_links_journal_operation_fk
        FOREIGN KEY (household_id, journal_operation_id)
        REFERENCES journal_operations (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT related_links_target_operation_fk
        FOREIGN KEY (household_id, target_operation_id)
        REFERENCES journal_operations (household_id, id) ON DELETE RESTRICT,
    CONSTRAINT related_links_target_plan_fk
        FOREIGN KEY (household_id, target_plan_id)
        REFERENCES plans (household_id, id) ON DELETE RESTRICT
);

CREATE INDEX related_links_journal_operation_idx ON related_links (journal_operation_id);
CREATE INDEX related_links_target_operation_idx
    ON related_links (target_operation_id) WHERE target_operation_id IS NOT NULL;
CREATE INDEX related_links_target_plan_idx
    ON related_links (target_plan_id) WHERE target_plan_id IS NOT NULL;

-- Idempotency records bind a household-scoped key to the request fingerprint
-- and the original committed operation. The composite primary key is the
-- household-scoped key space; its leading household_id column also serves the
-- per-household lookup, so no separate household index is needed.
CREATE TABLE idempotency_records (
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) > 0),
    request_fingerprint text NOT NULL CHECK (length(btrim(request_fingerprint)) > 0),
    operation_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (household_id, idempotency_key),
    CONSTRAINT idempotency_records_operation_fk
        FOREIGN KEY (household_id, operation_id)
        REFERENCES journal_operations (household_id, id) ON DELETE RESTRICT
);

CREATE INDEX idempotency_records_operation_idx ON idempotency_records (operation_id);

-- +goose Down
-- Test-teardown only: production rollback must be an explicit reviewed
-- decision because it destroys financial history. Drop in reverse dependency
-- order.
DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS related_links;
DROP TABLE IF EXISTS reservation_changes;
DROP TABLE IF EXISTS envelope_postings;
DROP TABLE IF EXISTS account_postings;
DROP TABLE IF EXISTS suggestions;
DROP TABLE IF EXISTS plans;
DROP TABLE IF EXISTS journal_operations;
DROP TABLE IF EXISTS envelopes;
DROP TABLE IF EXISTS accounts;
DROP TABLE IF EXISTS household_memberships;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS households;
