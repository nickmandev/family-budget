#!/usr/bin/env bash
#
# Encrypted, database-aware backup for the family-budget Compose stack.
#
# The primary artifact is a `pg_dump` custom-format archive produced inside the
# running `db` container. It is never a raw copy of the PostgreSQL data
# directory. The archive is checksummed, encrypted with `age` (public-key
# encryption: the VPS only ever holds the public recipient) and only then
# copied off-host with rsync over SSH to the configured target (default
# proposal: a Hetzner Storage Box). See docs/RUNBOOK.md.
#
# Usage: scripts/backup.sh [--no-upload] [--keep-local N] [--dry-run] [--help]
#
# Configuration is read from the process environment first, then from
# $ENV_FILE (default .env). Explicitly exported variables win over .env values.
# Never put the age private identity on the VPS.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

log() { printf '[backup] %s\n' "$*" >&2; }
warn() { printf '[backup] WARN: %s\n' "$*" >&2; }
die() {
	printf '[backup] ERROR: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: scripts/backup.sh [OPTIONS]

Create a pg_dump custom-format backup, encrypt it with age, optionally upload
it off-host with rsync over SSH, and prune old local/remote artifacts.

Options:
  --no-upload        Skip the off-host upload and remote pruning (local only).
  --keep-local N     Keep the newest N local .dump.age artifacts (overrides
                     BACKUP_KEEP_LOCAL). 0 disables local pruning.
  --dry-run          Validate configuration and print the planned commands
                     without touching the database, filesystem or network.
  -h, --help         Show this help and exit.

Environment (defaults match compose.yaml):
  ENV_FILE                     Environment file to read (default: .env).
  COMPOSE_PROJECT_NAME         Compose project name (default: family-budget).
  BACKUP_DB_SERVICE            Database service name (default: db).
  POSTGRES_USER                Database superuser used for pg_dump (required).
  POSTGRES_DB                  Database to dump (required).
  BACKUP_LOCAL_DIR             Local artifact directory (default: ./backups).
  BACKUP_ENCRYPTION_PUBLIC_KEY age public recipient (age1...) (required).
  BACKUP_KEEP_LOCAL            Local artifacts to keep (default: 8).
  BACKUP_SSH_TARGET            user@host of the off-host target (required
                               unless --no-upload).
  BACKUP_SSH_PORT              SSH port for the target (default: 23).
  BACKUP_SSH_KEY               SSH private key path (default: ssh agent/config).
  BACKUP_REMOTE_PATH           Remote directory (default: family-budget).
  BACKUP_KEEP_REMOTE           Remote artifacts to keep (default: 28).

Output: a machine-readable `backup_summary ...` line on stdout. A non-zero
exit means any step failed; the artifact is not uploaded unless it was
successfully dumped, checksummed and encrypted.
EOF
}

# --- configuration ---------------------------------------------------------

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

NO_UPLOAD=0
DRY_RUN=0
KEEP_LOCAL_OVERRIDE=""

while [[ $# -gt 0 ]]; do
	case "$1" in
	--no-upload) NO_UPLOAD=1 ;;
	--dry-run) DRY_RUN=1 ;;
	--keep-local)
		[[ $# -ge 2 ]] || die "--keep-local requires a value"
		KEEP_LOCAL_OVERRIDE="$2"
		shift
		;;
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
: "${BACKUP_LOCAL_DIR:=./backups}"
: "${BACKUP_KEEP_LOCAL:=8}"
: "${BACKUP_SSH_PORT:=23}"
: "${BACKUP_REMOTE_PATH:=family-budget}"
: "${BACKUP_KEEP_REMOTE:=28}"
POSTGRES_USER="${POSTGRES_USER:-}"
POSTGRES_DB="${POSTGRES_DB:-}"
BACKUP_ENCRYPTION_PUBLIC_KEY="${BACKUP_ENCRYPTION_PUBLIC_KEY:-}"
BACKUP_SSH_TARGET="${BACKUP_SSH_TARGET:-}"
BACKUP_SSH_KEY="${BACKUP_SSH_KEY:-}"

[[ -n "$POSTGRES_USER" ]] || die "POSTGRES_USER is not set (export it or add it to $ENV_FILE)"
[[ -n "$POSTGRES_DB" ]] || die "POSTGRES_DB is not set (export it or add it to $ENV_FILE)"
[[ -n "$BACKUP_ENCRYPTION_PUBLIC_KEY" ]] || die "BACKUP_ENCRYPTION_PUBLIC_KEY is not set; backups must be encrypted before transfer"

KEEP_LOCAL="${KEEP_LOCAL_OVERRIDE:-$BACKUP_KEEP_LOCAL}"
[[ "$KEEP_LOCAL" =~ ^[0-9]+$ ]] || die "--keep-local/BACKUP_KEEP_LOCAL must be a non-negative integer"
[[ "$BACKUP_KEEP_REMOTE" =~ ^[0-9]+$ ]] || die "BACKUP_KEEP_REMOTE must be a non-negative integer"

if (( ! NO_UPLOAD )); then
	[[ -n "$BACKUP_SSH_TARGET" ]] || die "BACKUP_SSH_TARGET is not set; use --no-upload for a local-only run"
fi

# --- helpers ---------------------------------------------------------------

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

# shquote renders a single-quoted POSIX shell word for the remote host.
shquote() {
	printf "'%s'" "${1//\'/\'\\\'\'}"
}

COMPOSE=(docker compose -p "$COMPOSE_PROJECT_NAME")

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$BACKUP_SSH_PORT")
if [[ -n "$BACKUP_SSH_KEY" ]]; then
	SSH_OPTS+=(-i "$BACKUP_SSH_KEY")
fi
SSH_CMD="ssh ${SSH_OPTS[*]}"

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BASE="family-budget_${TIMESTAMP}"
LOCAL_ENC="${BACKUP_LOCAL_DIR}/${BASE}.dump.age"
REMOTE_LOCATION="none"
if (( ! NO_UPLOAD )); then
	REMOTE_LOCATION="${BACKUP_SSH_TARGET}:${BACKUP_REMOTE_PATH}/${BASE}.dump.age"
fi

# --- dry run ---------------------------------------------------------------

if (( DRY_RUN )); then
	log "dry run: no database, filesystem or network changes will be made"
	log "would run: ${COMPOSE[*]} exec -T ${BACKUP_DB_SERVICE} pg_dump -U ${POSTGRES_USER} -d ${POSTGRES_DB} --format=custom > ${BASE}.dump"
	log "would compute: sha256sum ${BASE}.dump"
	log "would run: age --encrypt --recipient <redacted> --output ${BASE}.dump.age ${BASE}.dump"
	log "would record: ${BASE}.dump.age.sha256 and ${BASE}.meta in ${BACKUP_LOCAL_DIR}"
	if (( ! NO_UPLOAD )); then
		log "would upload with: rsync -az --partial -e 'ssh ...' ${BASE}.dump.age* ${BASE}.meta ${BACKUP_SSH_TARGET}:${BACKUP_REMOTE_PATH}/"
	fi
	printf 'backup_summary dry_run=true path=%s size_bytes=0 sha256=none remote=%s\n' \
		"$LOCAL_ENC" "$REMOTE_LOCATION"
	exit 0
fi

require_cmd docker
require_cmd age
require_cmd sha256sum
if (( ! NO_UPLOAD )); then
	require_cmd rsync
	require_cmd ssh
fi

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/family-budget-backup.XXXXXX")"
cleanup() { rm -rf -- "$WORK_DIR"; }
trap cleanup EXIT

[[ -e "$LOCAL_ENC" ]] && die "refusing to overwrite existing artifact: $LOCAL_ENC"

mkdir -p -- "$BACKUP_LOCAL_DIR"

DUMP_FILE="${WORK_DIR}/${BASE}.dump"
ENC_FILE="${WORK_DIR}/${BASE}.dump.age"
SHA_FILE="${ENC_FILE}.sha256"
META_FILE="${WORK_DIR}/${BASE}.meta"

# --- 1/2. database-aware dump ---------------------------------------------

log "dumping database ${POSTGRES_DB} from service ${BACKUP_DB_SERVICE}"
"${COMPOSE[@]}" exec -T "$BACKUP_DB_SERVICE" \
	pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom --no-password \
	>"$DUMP_FILE"

[[ -s "$DUMP_FILE" ]] || die "pg_dump produced an empty archive"

# --- 3. checksum -----------------------------------------------------------

PLAIN_SHA="$(sha256sum "$DUMP_FILE" | awk '{print $1}')"
[[ "$PLAIN_SHA" =~ ^[0-9a-f]{64}$ ]] || die "could not compute the dump checksum"
log "plaintext sha256: $PLAIN_SHA"

# --- 4. encryption before any transfer ------------------------------------

log "encrypting with age (public-key recipient from the environment)"
age --encrypt --recipient "$BACKUP_ENCRYPTION_PUBLIC_KEY" \
	--output "$ENC_FILE" "$DUMP_FILE"
[[ -s "$ENC_FILE" ]] || die "age produced an empty encrypted archive"

# The checksum covers the encrypted artifact so restore can verify the bytes
# it received. Record the format `sha256  filename` so `sha256sum -c` works.
(
	cd -- "$WORK_DIR"
	sha256sum -- "$(basename -- "$ENC_FILE")" >"$(basename -- "$SHA_FILE")"
)
ENC_SHA="$(sha256sum "$ENC_FILE" | awk '{print $1}')"
SIZE_BYTES="$(wc -c <"$ENC_FILE" | tr -d '[:space:]')"

{
	printf 'format=age\n'
	printf 'created_at=%s\n' "$TIMESTAMP"
	printf 'postgres_db=%s\n' "$POSTGRES_DB"
	printf 'artifact=%s\n' "$(basename -- "$ENC_FILE")"
	printf 'plaintext_sha256=%s\n' "$PLAIN_SHA"
	printf 'encrypted_sha256=%s\n' "$ENC_SHA"
	printf 'size_bytes=%s\n' "$SIZE_BYTES"
} >"$META_FILE"

mv -- "$ENC_FILE" "$SHA_FILE" "$META_FILE" "$BACKUP_LOCAL_DIR/"
log "wrote ${LOCAL_ENC}"

# --- 5. local retention ----------------------------------------------------

prune_local() {
	local keep="$1" dir="$2"
	(( keep > 0 )) || return 0
	shopt -s nullglob
	local -a candidates=("$dir"/family-budget_*.dump.age)
	shopt -u nullglob
	(( ${#candidates[@]} > keep )) || return 0

	mapfile -t candidates < <(ls -1t -- "${candidates[@]}")
	local i file base
	for ((i = keep; i < ${#candidates[@]}; i++)); do
		file="${candidates[i]}"
		base="${file%.dump.age}"
		rm -f -- "$file" "${file}.sha256" "${base}.meta"
		log "pruned local artifact $(basename -- "$file")"
	done
}
prune_local "$KEEP_LOCAL" "$BACKUP_LOCAL_DIR"

# --- 6/7. off-host upload and remote retention ----------------------------

if (( NO_UPLOAD )); then
	log "upload skipped (--no-upload)"
else
	REMOTE_PATH_Q="$(shquote "$BACKUP_REMOTE_PATH")"
	log "ensuring remote directory ${BACKUP_SSH_TARGET}:${BACKUP_REMOTE_PATH}"
	ssh "${SSH_OPTS[@]}" "$BACKUP_SSH_TARGET" "mkdir -p -- ${REMOTE_PATH_Q}"

	log "uploading ${BASE}.dump.age to ${BACKUP_SSH_TARGET}:${BACKUP_REMOTE_PATH}"
	rsync -az --partial -e "$SSH_CMD" \
		"$LOCAL_ENC" "${LOCAL_ENC}.sha256" "${BACKUP_LOCAL_DIR}/${BASE}.meta" \
		"${BACKUP_SSH_TARGET}:${BACKUP_REMOTE_PATH}/"

	if (( BACKUP_KEEP_REMOTE > 0 )); then
		log "pruning remote artifacts beyond ${BACKUP_KEEP_REMOTE}"
		remote_prune=$(
			cat <<REMOTE
cd -- ${REMOTE_PATH_Q} && \
ls -1t family-budget_*.dump.age 2>/dev/null | tail -n +$((BACKUP_KEEP_REMOTE + 1)) | while IFS= read -r f; do
  rm -f -- "\$f" "\$f.sha256" "\${f%.dump.age}.meta"
done
REMOTE
		)
		ssh "${SSH_OPTS[@]}" "$BACKUP_SSH_TARGET" "$remote_prune"
	fi
fi

printf 'backup_summary path=%s size_bytes=%s sha256=%s remote=%s\n' \
	"$LOCAL_ENC" "$SIZE_BYTES" "$ENC_SHA" "$REMOTE_LOCATION"
log "backup complete"
