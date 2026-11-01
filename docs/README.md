# Household Budgeting Agent Pack

Start here when handing this specification to engineering agents.

## Canonical domain contract

- [`ACCOUNTING_SPEC.md`](ACCOUNTING_SPEC.md) — product decisions, accounting vocabulary, fundamental balance identity, defaults, and baseline acceptance criteria.
- [`POSTING_MATRIX.md`](POSTING_MATRIX.md) — precise account/liability and budget effects for each event type, authorization rules, reservations, reversals, reconciliation, and failure semantics.
- [`STATE_TRANSITIONS.md`](STATE_TRANSITIONS.md) — purchase-plan and suggestion lifecycles, correction/refund semantics, allowed transitions, and invalid transition behavior.
- [`DOMAIN_MODEL.md`](DOMAIN_MODEL.md) — framework-neutral entities, value objects, commands, domain events, consistency boundaries, and derived query models.
- [`DECISIONS.yaml`](DECISIONS.yaml) — compact machine-readable index of the selected defaults.
- [`AGENT_BRIEF.md`](AGENT_BRIEF.md) — implementation mission, constraints, and expected deliverables.
- [`STACK_DECISIONS.md`](STACK_DECISIONS.md) — confirmed Go/GORM/PostgreSQL/`net/http` stack, migration discipline, and testing strategy.
- [`ENABLE_BANKING_INTEGRATION_PLAN.md`](ENABLE_BANKING_INTEGRATION_PLAN.md) — proposed post-MVP AIS integration architecture, accounting boundaries, rollout gates, and open approvals.

## Recommended reading order for agents

1. `AGENT_BRIEF.md`
2. `ACCOUNTING_SPEC.md`
3. `POSTING_MATRIX.md`
4. `STATE_TRANSITIONS.md`
5. `DOMAIN_MODEL.md`
6. `DECISIONS.yaml` for a concise decision lookup
7. `STACK_DECISIONS.md` for the confirmed implementation stack and remaining open choices

## Suggested implementation sequence

1. Review all canonical docs, including `DOMAIN_MODEL.md`, and report genuine contradictions or undecided defaults.
2. Implement validated value objects and pure posting/calculation logic.
3. Implement command handlers and state-transition guards as atomic operations.
4. Follow `STACK_DECISIONS.md` for code boundaries. Go, GORM, PostgreSQL 18.x, and `net/http` are confirmed; the migration tool remains undecided.
5. Add unit tests for every posting-matrix row and every allowed/rejected transition.
6. Add randomized/property-based operation sequences to validate balance identities, reservations, corrections, and boundary transfers.
7. Add concurrency/idempotency tests and projection-vs-journal rebuild checks.
8. Only then finalize schema/API design or commit framework-specific details.

## Scope note

This pack describes the agreed design intent. It is not executable application code, and the acceptance tests have not yet been implemented or run. If an implementation needs to deviate from a rule, require a written proposal with an example, rationale, and test impact before changing the canonical spec.
