#!/usr/bin/env bash
#
# Restore a family-budget backup into a CLEAN database.
#
# Documented migration order (docs/RUNBOOK.md): restore the pg_dump archive into
# a freshly created database, then run `goose up` for any newer migrations, then
# start the API and verify. The API binary never migrates on startup.
#
# Because this is destructive it stops `api`, drops and recreates the target
# database, restores, migrates, starts `api` and runs verify-restore.sh. It
# requires an explicit confirmation unless --force is given.
#
# Usage: scripts/restore.sh <artifact.dump.age> [--force] [--require-data] [--help]
#        scripts/restore.sh --fetch-latest [--force] [--require-data] [--help]

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

log() { printf '[restore] %s\n' "$*" >&2; }
die() {
	printf '[restore] ERROR: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: scripts/restore.sh ARTIFACT [OPTIONS]
       scripts/restore.sh --fetch-latest [OPTIONS]

Restore an age-encrypted pg_dump custom-format backup into a freshly created
database, apply migrations, start the API and verify the result.

Arguments:
  ARTIFACT             Path to a family-budget_*.dump.age artifact.

Options:
  --fetch-latest       Fetch the newest artifact from the off-host target first.
  --force              Skip the interactive confirmation (required for scripts
                       and non-interactive runs).
  --require-data       Verify that restored key tables contain data (use for a
                       real backup; empty databases fail).
  -h, --help           Show this help and exit.

Environment (defaults match compose.yaml):
  ENV_FILE                 Environment file to read (default: .env).
  COMPOSE_PROJECT_NAME     Compose project name (default: family-budget).
  BACKUP_DB_SERVICE        Database service name (default: db).
  BACKUP_API_SERVICE       API service name (default: api).
  POSTGRES_USER            Database owner/superuser (required).
  POSTGRES_DB              Database to recreate and restore (required).
  BACKUP_LOCAL_DIR         Local artifact directory (default: ./backups).
  BACKUP_AGE_IDENTITY_FILE age private identity on THIS machine (required).
  BACKUP_SSH_TARGET        Off-host target for --fetch-latest (required then).
  BACKUP_SSH_PORT          SSH port for the target (default: 23).
  BACKUP_SSH_KEY           SSH private key path (default: ssh agent/config).
  BACKUP_REMOTE_PATH       Remote directory (default: family-budget).
  PORT                     API port inside the container (default: 8080).
  BACKUP_READYZ_URL        Optional URL for the readiness check; when unset the
                           script probes /readyz from inside the api container.

Confirmation: without --force you must type RESTORE. The script then stops
api, runs DROP DATABASE ... WITH (FORCE) and CREATE DATABASE, restores the
archive, runs `docker compose run --rm migrate up`, starts api and runs
scripts/verify-restore.sh.
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

ARTIFACT=""
FETCH_LATEST=0
FORCE=0
REQUIRE_DATA=0

while [[ $# -gt 0 ]]; do
	case "$1" in
	--fetch-latest) FETCH_LATEST=1 ;;
	--force) FORCE=1 ;;
	--require-data) REQUIRE_DATA=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	-*)
		die "unknown option: $1 (see --help)"
		;;
	*)
		[[ -z "$ARTIFACT" ]] || die "only one artifact may be given"
		ARTIFACT="$1"
		;;
	esac
	shift
done

if (( FETCH_LATEST )) && [[ -n "$ARTIFACT" ]]; then
	die "use either an explicit artifact or --fetch-latest, not both"
fi
if (( ! FETCH_LATEST )) && [[ -z "$ARTIFACT" ]]; then
	die "an artifact path or --fetch-latest is required (see --help)"
fi

ENV_FILE="${ENV_FILE:-.env}"
load_env_file "$ENV_FILE"

: "${COMPOSE_PROJECT_NAME:=family-budget}"
: "${BACKUP_DB_SERVICE:=db}"
: "${BACKUP_API_SERVICE:=api}"
: "${BACKUP_LOCAL_DIR:=./backups}"
: "${BACKUP_SSH_PORT:=23}"
: "${BACKUP_REMOTE_PATH:=family-budget}"
: "${PORT:=8080}"
POSTGRES_USER="${POSTGRES_USER:-}"
POSTGRES_DB="${POSTGRES_DB:-}"
BACKUP_AGE_IDENTITY_FILE="${BACKUP_AGE_IDENTITY_FILE:-}"
BACKUP_SSH_TARGET="${BACKUP_SSH_TARGET:-}"
BACKUP_SSH_KEY="${BACKUP_SSH_KEY:-}"

[[ -n "$POSTGRES_USER" ]] || die "POSTGRES_USER is not set (export it or add it to $ENV_FILE)"
[[ -n "$POSTGRES_DB" ]] || die "POSTGRES_DB is not set (export it or add it to $ENV_FILE)"
[[ -n "$BACKUP_AGE_IDENTITY_FILE" ]] || die "BACKUP_AGE_IDENTITY_FILE is not set; the age private identity is required to decrypt"
[[ -f "$BACKUP_AGE_IDENTITY_FILE" ]] || die "age identity not found: $BACKUP_AGE_IDENTITY_FILE"

# Database identifiers are interpolated into SQL. Reject anything that is not a
# plain PostgreSQL identifier rather than risk quote/semicolon injection.
for ident in "$POSTGRES_USER" "$POSTGRES_DB"; do
	[[ "$ident" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || die "unsafe database identifier: $ident"
done

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}
require_cmd docker
require_cmd age
require_cmd sha256sum

COMPOSE=(docker compose -p "$COMPOSE_PROJECT_NAME")

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$BACKUP_SSH_PORT")
if [[ -n "$BACKUP_SSH_KEY" ]]; then
	SSH_OPTS+=(-i "$BACKUP_SSH_KEY")
fi
SSH_CMD="ssh ${SSH_OPTS[*]}"

shquote() {
	printf "'%s'" "${1//\'/\'\\\'\'}"
}

# --- 1. fetch latest (optional) -------------------------------------------

if (( FETCH_LATEST )); then
	require_cmd rsync
	require_cmd ssh
	[[ -n "$BACKUP_SSH_TARGET" ]] || die "--fetch-latest requires BACKUP_SSH_TARGET"
	mkdir -p -- "$BACKUP_LOCAL_DIR"

	REMOTE_PATH_Q="$(shquote "$BACKUP_REMOTE_PATH")"
	remote_latest="$(
		ssh "${SSH_OPTS[@]}" "$BACKUP_SSH_TARGET" \
			"ls -1t ${REMOTE_PATH_Q}/family-budget_*.dump.age 2>/dev/null | head -n 1"
	)" || die "could not list backups on ${BACKUP_SSH_TARGET}:${BACKUP_REMOTE_PATH}"
	[[ -n "$remote_latest" ]] || die "no backups found on ${BACKUP_SSH_TARGET}:${BACKUP_REMOTE_PATH}"

	log "fetching newest artifact $(basename -- "$remote_latest")"
	rsync -az --partial -e "$SSH_CMD" \
		"${BACKUP_SSH_TARGET}:${remote_latest}" \
		"${BACKUP_SSH_TARGET}:${remote_latest}.sha256" \
		"$BACKUP_LOCAL_DIR/"
	# The metadata sidecar is optional; older artifacts may not have one.
	rsync -az --partial -e "$SSH_CMD" \
		"${BACKUP_SSH_TARGET}:${remote_latest%.dump.age}.meta" \
		"$BACKUP_LOCAL_DIR/" 2>/dev/null ||
		log "no metadata sidecar for $(basename -- "$remote_latest") (continuing)"
	ARTIFACT="${BACKUP_LOCAL_DIR}/$(basename -- "$remote_latest")"
fi

# --- 2. checksum + decrypt ------------------------------------------------

[[ -f "$ARTIFACT" ]] || die "artifact not found: $ARTIFACT"
ARTIFACT_DIR="$(cd -- "$(dirname -- "$ARTIFACT")" >/dev/null 2>&1 && pwd)"
ARTIFACT_BASE="$(basename -- "$ARTIFACT")"
ARTIFACT="${ARTIFACT_DIR}/${ARTIFACT_BASE}"
SHA_FILE="${ARTIFACT}.sha256"

[[ -f "$SHA_FILE" ]] || die "missing checksum file: $SHA_FILE (refusing to restore unverified bytes)"
log "verifying sha256 of $(basename -- "$ARTIFACT")"
(
	cd -- "$ARTIFACT_DIR"
	sha256sum -c --strict --quiet -- "$ARTIFACT_BASE"
) || die "checksum verification failed for $ARTIFACT"

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/family-budget-restore.XXXXXX")"
cleanup() { rm -rf -- "$WORK_DIR"; }
trap cleanup EXIT

DECRYPTED="${WORK_DIR}/${ARTIFACT_BASE%.dump.age}.dump"
log "decrypting with the age identity"
age --decrypt --identity "$BACKUP_AGE_IDENTITY_FILE" --output "$DECRYPTED" "$ARTIFACT"
[[ -s "$DECRYPTED" ]] || die "decryption produced an empty archive"

META_FILE="${ARTIFACT%.dump.age}.meta"
if [[ -f "$META_FILE" ]]; then
	want_plain="$(sed -n 's/^plaintext_sha256=//p' "$META_FILE" | head -n 1)"
	if [[ -n "$want_plain" ]]; then
		got_plain="$(sha256sum "$DECRYPTED" | awk '{print $1}')"
		[[ "$want_plain" == "$got_plain" ]] ||
			die "decrypted dump checksum does not match metadata (expected $want_plain, got $got_plain)"
		log "decrypted dump checksum matches metadata"
	fi
else
	log "no metadata sidecar; skipped the plaintext checksum cross-check"
fi

# --- 3. explicit confirmation --------------------------------------------

if (( ! FORCE )); then
	[[ -t 0 ]] || die "stdin is not a terminal; pass --force for a non-interactive restore"
	printf '' >&2
	printf 'This will DROP and recreate database "%s" on the "%s" service and stop "%s".\n' \
		"$POSTGRES_DB" "$BACKUP_DB_SERVICE" "$BACKUP_API_SERVICE" >&2
	printf 'Type RESTORE to continue: ' >&2
	read -r confirmation
	[[ "$confirmation" == "RESTORE" ]] || die "aborted by operator"
fi

# --- 4. clean database restore -------------------------------------------

log "stopping ${BACKUP_API_SERVICE}"
"${COMPOSE[@]}" stop "$BACKUP_API_SERVICE" >/dev/null 2>&1 ||
	log "api was not running (continuing)"

log "recreating database ${POSTGRES_DB} (destructive)"
"${COMPOSE[@]}" exec -T "$BACKUP_DB_SERVICE" \
	psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
	-c "DROP DATABASE IF EXISTS \"${POSTGRES_DB}\" WITH (FORCE);" \
	-c "CREATE DATABASE \"${POSTGRES_DB}\" OWNER \"${POSTGRES_USER}\";"

log "restoring archive with pg_restore"
"${COMPOSE[@]}" exec -T "$BACKUP_DB_SERVICE" \
	pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
	--exit-on-error --single-transaction <"$DECRYPTED"

# --- 5. migrations after the dump, then application cutover --------------

log "applying migrations newer than the dump (goose up)"
"${COMPOSE[@]}" run --rm migrate up

log "starting ${BACKUP_API_SERVICE}"
"${COMPOSE[@]}" up -d "$BACKUP_API_SERVICE"

# --- 6. post-restore verification ----------------------------------------

log "running post-restore checks"
VERIFY_ARGS=()
if (( REQUIRE_DATA )); then
	VERIFY_ARGS+=(--require-data)
fi
bash "${SCRIPT_DIR}/verify-restore.sh" "${VERIFY_ARGS[@]}"

log "restore complete"
