#!/usr/bin/env bash
#
# Verify that a restored family-budget database and API are healthy.
#
# A successful pg_restore is not evidence that the restored instance works
# (SECURITY_AND_OPERATIONS "A successful backup command is not sufficient;
# restoration must be verified"). This script checks:
#
#   1. PostgreSQL accepts connections and answers SELECT 1.
#   2. The expected schema objects exist.
#   3. Spot-check row counts for households, journal_operations,
#      account_postings, envelope_postings and idempotency_records.
#   4. A lightweight consistency check: households.ledger_version equals the
#      number of committed journal operations for that household. This is the
#      SQL-expressible core of the Go adapter's full VerifyProjections; the
#      complete account/envelope balance rebuild is documented as deferred to
#      the integration-test suite in docs/RUNBOOK.md.
#   5. /readyz answers once the API is up.
#
# Usage: scripts/verify-restore.sh [--require-data] [--help]

set -euo pipefail

log() { printf '[verify] %s\n' "$*" >&2; }
die() {
	printf '[verify] ERROR: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: scripts/verify-restore.sh [OPTIONS]

Verify a restored database and the running API.

Options:
  --require-data   Fail unless households, journal_operations and
                   account_postings contain at least one row. Use after
                   restoring a real backup; omit for an intentionally empty
                   database.
  -h, --help       Show this help and exit.

Environment (defaults match compose.yaml):
  ENV_FILE               Environment file to read (default: .env).
  COMPOSE_PROJECT_NAME   Compose project name (default: family-budget).
  BACKUP_DB_SERVICE      Database service name (default: db).
  BACKUP_API_SERVICE     API service name (default: api).
  POSTGRES_USER          Database user (required).
  POSTGRES_DB            Database name (required).
  PORT                   API port inside the container (default: 8080).
  BACKUP_READYZ_URL      Optional URL probed with curl. When unset, the script
                         probes /readyz from inside the api container.
EOF
}

load_env_file() {
	local file="$1"
	[[ -f "$file" ]] || return 0
	local line key value
	while IFS= read -r line || [[ -n "$line" ]]; do
		line="${line#"${line%%[![:space:]]*}"}"
		[[ -z "$line" || "$line" == \#* ]] && continue
		[[ "$line" != *=* ]] && continue
		key="${line%%=*}"
		value="${line#*=}"
		key="${key//[[:space:]]/}"
		[[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
		if [[ -z "${!key+x}" ]]; then
			export "$key=$value"
		fi
	done <"$file"
}

REQUIRE_DATA=0
while [[ $# -gt 0 ]]; do
	case "$1" in
	--require-data) REQUIRE_DATA=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown argument: $1 (see --help)" ;;
	esac
	shift
done

ENV_FILE="${ENV_FILE:-.env}"
load_env_file "$ENV_FILE"

: "${COMPOSE_PROJECT_NAME:=family-budget}"
: "${BACKUP_DB_SERVICE:=db}"
: "${BACKUP_API_SERVICE:=api}"
: "${PORT:=8080}"
POSTGRES_USER="${POSTGRES_USER:-}"
POSTGRES_DB="${POSTGRES_DB:-}"
BACKUP_READYZ_URL="${BACKUP_READYZ_URL:-}"

[[ -n "$POSTGRES_USER" ]] || die "POSTGRES_USER is not set (export it or add it to $ENV_FILE)"
[[ -n "$POSTGRES_DB" ]] || die "POSTGRES_DB is not set (export it or add it to $ENV_FILE)"

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}
require_cmd docker

COMPOSE=(docker compose -p "$COMPOSE_PROJECT_NAME")

psql_db() {
	"${COMPOSE[@]}" exec -T "$BACKUP_DB_SERVICE" \
		psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 "$@"
}

# --- 1. connectivity -------------------------------------------------------

log "checking pg_isready"
"${COMPOSE[@]}" exec -T "$BACKUP_DB_SERVICE" \
	pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null ||
	die "PostgreSQL is not accepting connections on service ${BACKUP_DB_SERVICE}"

log "checking SELECT 1"
select_one="$(psql_db -tAc 'SELECT 1;' | tr -d '[:space:]')"
[[ "$select_one" == "1" ]] || die "SELECT 1 returned '${select_one}' instead of 1"

# --- 2. schema presence ----------------------------------------------------

required_tables=(households journal_operations account_postings envelope_postings idempotency_records)
table_values=""
for table in "${required_tables[@]}"; do
	table_values+="('${table}'),"
done
table_values="${table_values%,}"

missing="$(
	psql_db -tAc "SELECT coalesce(string_agg(t, ',' ORDER BY t), '') FROM (VALUES ${table_values}) AS v(t) WHERE to_regclass('public.' || t) IS NULL;"
)"
if [[ -n "$missing" ]]; then
	die "restored database is missing table(s): ${missing}. Run 'docker compose run --rm migrate up' and retry."
fi
log "schema tables present"

# --- 3. row-count spot checks ---------------------------------------------

counts="$(
	psql_db -tA -F',' -c "
		SELECT
			(SELECT count(*) FROM households),
			(SELECT count(*) FROM journal_operations),
			(SELECT count(*) FROM account_postings),
			(SELECT count(*) FROM envelope_postings),
			(SELECT count(*) FROM idempotency_records);"
)"
IFS=',' read -r households journal_ops account_postings envelope_postings idempotency_records <<<"$counts"

log "rows: households=${households} journal_operations=${journal_ops} account_postings=${account_postings} envelope_postings=${envelope_postings} idempotency_records=${idempotency_records}"

for pair in "households:${households}" "journal_operations:${journal_ops}" "account_postings:${account_postings}" \
	"envelope_postings:${envelope_postings}" "idempotency_records:${idempotency_records}"; do
	[[ "${pair##*:}" =~ ^[0-9]+$ ]] || die "unexpected row count for ${pair%%:*}: ${pair##*:}"
done

if (( REQUIRE_DATA )); then
	(( households > 0 )) || die "--require-data: households is empty; the backup may be schema-only"
	(( journal_ops > 0 )) || die "--require-data: journal_operations is empty; the backup may be schema-only"
	(( account_postings > 0 )) || die "--require-data: account_postings is empty; the backup may be schema-only"
	log "data presence checks passed"
fi

# --- 4. journal/projection consistency spot check -------------------------

# households.ledger_version is materialized from the committed journal. A
# mismatch means the projection is stale or the restore was partial. The full
# balance rebuild (VerifyProjections) needs the Go adapter; see RUNBOOK.md.
mismatched="$(
	psql_db -tAc "
		SELECT count(*) FROM households h
		WHERE h.ledger_version <> (
			SELECT count(*) FROM journal_operations j WHERE j.household_id = h.id
		);"
)"
[[ "$mismatched" == "0" ]] ||
	die "${mismatched} household(s) have ledger_version != committed journal operation count; investigate before cutover"

log "ledger_version matches the journal operation count"

# --- 5. API readiness ------------------------------------------------------

if [[ -n "$BACKUP_READYZ_URL" ]]; then
	require_cmd curl
	log "checking ${BACKUP_READYZ_URL}"
	curl -fsS --max-time 10 "$BACKUP_READYZ_URL" >/dev/null ||
		die "readiness probe failed: ${BACKUP_READYZ_URL}"
else
	log "checking /readyz inside the ${BACKUP_API_SERVICE} container"
	"${COMPOSE[@]}" exec -T "$BACKUP_API_SERVICE" \
		wget -q -O - "http://127.0.0.1:${PORT}/readyz" >/dev/null ||
		die "readiness probe failed; is ${BACKUP_API_SERVICE} running and healthy? (docker compose ps)"
fi

log "verification passed"
