#!/usr/bin/env bash
#
# Provision a disposable PostgreSQL and run the persistence integration suite.
#
# The PostgreSQL adapter tests skip themselves when no database is configured.
# This script makes the real run one command: it starts a throwaway PostgreSQL
# (Docker, Podman, or a local initdb cluster), waits for it to accept
# connections, exports TEST_POSTGRES_DSN, runs the tests with -count=1, and
# removes the container/cluster on exit. Nothing is written to the repository.
#
# Usage: scripts/integration-test.sh [OPTIONS]
#
# The compose stack intentionally does not publish PostgreSQL to the host, so
# this runner always uses its own disposable instance (or an explicit DSN).
# See internal/adapters/postgres/README.md and docs/RUNBOOK.md.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." >/dev/null 2>&1 && pwd)"

log() { printf '[integration] %s\n' "$*" >&2; }
warn() { printf '[integration] WARN: %s\n' "$*" >&2; }
die() {
	printf '[integration] ERROR: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Usage: scripts/integration-test.sh [OPTIONS]

Provision a disposable PostgreSQL, run the integration suite against it, then
tear it down. Prefers Docker, then Podman, then a local initdb cluster. An
explicit DSN bypasses provisioning entirely.

Options:
  --engine NAME      Container engine to use: auto, docker, podman, or local
                     (default: auto; local means a temporary initdb cluster).
  --port N           Loopback port for the disposable server (default: 55432).
  --dsn URL          Use an already-running PostgreSQL instead of provisioning.
                     TEST_POSTGRES_DSN in the environment has the same effect.
  --all              Run every package (go test ./...) instead of only the
                     PostgreSQL adapter integration package.
  --race             Add -race to the test run.
  --keep-db          Leave the container/cluster running and print its DSN.
  -h, --help         Show this help and exit.

Environment:
  PG_IMAGE           PostgreSQL image for container engines
                     (default: postgres:18.6, matching compose.yaml).
  TEST_POSTGRES_DSN  Existing DSN to use instead of provisioning.
  TEST_DB_USER       Disposable database user (default: test).
  TEST_DB_PASSWORD   Disposable database password (default: test).
  TEST_DB_NAME       Disposable database name (default: test).

Examples:
  scripts/integration-test.sh
  scripts/integration-test.sh --race --all
  scripts/integration-test.sh --engine podman --port 55433
  TEST_POSTGRES_DSN='postgres://user:pass@host:5432/db?sslmode=disable' \
    scripts/integration-test.sh --all

A non-zero exit means the suite failed or the database could not be
provisioned. The runner never leaves a container or cluster behind unless
--keep-db is given.
EOF
}

ENGINE="auto"
PORT="55432"
KEEP_DB=0
RACE=0
RUN_ALL=0
EXTERNAL_DSN="${TEST_POSTGRES_DSN:-}"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--engine)
		[[ $# -ge 2 ]] || die "--engine requires a value"
		ENGINE="$2"
		shift
		;;
	--port)
		[[ $# -ge 2 ]] || die "--port requires a value"
		PORT="$2"
		shift
		;;
	--dsn)
		[[ $# -ge 2 ]] || die "--dsn requires a value"
		EXTERNAL_DSN="$2"
		shift
		;;
	--all) RUN_ALL=1 ;;
	--race) RACE=1 ;;
	--keep-db) KEEP_DB=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown argument: $1 (see --help)" ;;
	esac
	shift
done

[[ "$PORT" =~ ^[0-9]+$ ]] || die "--port must be a number"
(( PORT >= 1 && PORT <= 65535 )) || die "--port must be between 1 and 65535"

PG_IMAGE="${PG_IMAGE:-postgres:18.6}"
PG_USER="${TEST_DB_USER:-test}"
PG_PASSWORD="${TEST_DB_PASSWORD:-test}"
PG_DB="${TEST_DB_NAME:-test}"

CONTAINER="fb-pgtest-$$"
WORK_DIR=""
ENGINE_USED=""
PG_BIN=""
STARTED_CONTAINER=0
STARTED_CLUSTER=0
DSN=""

cleanup() {
	local status=$?
	set +e
	if (( KEEP_DB )); then
		if [[ -n "$DSN" ]]; then
			log "keeping database; TEST_POSTGRES_DSN=$DSN"
		fi
		exit "$status"
	fi
	if (( STARTED_CONTAINER )) && [[ -n "$ENGINE_USED" ]]; then
		"$ENGINE_USED" rm -f "$CONTAINER" >/dev/null 2>&1
	fi
	if (( STARTED_CLUSTER )) && [[ -n "$PG_BIN" && -n "$WORK_DIR" ]]; then
		"$PG_BIN/pg_ctl" -D "$WORK_DIR/data" -m fast stop >/dev/null 2>&1
	fi
	if [[ -n "$WORK_DIR" ]]; then
		rm -rf -- "$WORK_DIR"
	fi
	exit "$status"
}
trap cleanup EXIT

tcp_ready() {
	(exec 3<>"/dev/tcp/127.0.0.1/$PORT") >/dev/null 2>&1
}

wait_for_tcp() {
	local label="$1" i
	for i in $(seq 1 60); do
		if tcp_ready; then
			log "$label is accepting connections on 127.0.0.1:$PORT (after ${i}s)"
			return 0
		fi
		sleep 1
	done
	return 1
}

find_local_postgres() {
	local candidate dir
	if command -v initdb >/dev/null 2>&1; then
		dirname -- "$(command -v initdb)"
		return 0
	fi
	for dir in /usr/lib/postgresql/*/bin /usr/pgsql-*/bin; do
		if [[ -x "$dir/initdb" && -x "$dir/pg_ctl" ]]; then
			printf '%s\n' "$dir"
			return 0
		fi
	done
	return 1
}

container_engine_available() {
	local engine="$1"
	command -v "$engine" >/dev/null 2>&1 && "$engine" info >/dev/null 2>&1
}

resolve_engine() {
	case "$ENGINE" in
	auto)
		if container_engine_available docker; then
			printf 'docker\n'
			return 0
		fi
		if container_engine_available podman; then
			printf 'podman\n'
			return 0
		fi
		if find_local_postgres >/dev/null 2>&1; then
			printf 'local\n'
			return 0
		fi
		return 1
		;;
	docker | podman)
		container_engine_available "$ENGINE" || return 1
		printf '%s\n' "$ENGINE"
		;;
	local)
		find_local_postgres >/dev/null 2>&1 || return 1
		printf 'local\n'
		;;
	*) return 1 ;;
	esac
}

start_container() {
	local engine="$1"
	ENGINE_USED="$engine"

	if tcp_ready; then
		die "127.0.0.1:$PORT is already in use; pass --port with a free port"
	fi

	log "starting $PG_IMAGE as $CONTAINER with $engine"
	if "$engine" run --rm -d --name "$CONTAINER" \
		-e POSTGRES_USER="$PG_USER" \
		-e POSTGRES_PASSWORD="$PG_PASSWORD" \
		-e POSTGRES_DB="$PG_DB" \
		-p "127.0.0.1:${PORT}:5432" \
		"$PG_IMAGE" >/dev/null; then
		STARTED_CONTAINER=1
		if wait_for_tcp "$CONTAINER"; then
			return 0
		fi
		# Rootless Docker (and some Podman setups) cannot publish ports through
		# pasta/slirp. Retry with host networking below.
		warn "published port is unreachable; retrying with host networking"
		"$engine" rm -f "$CONTAINER" >/dev/null 2>&1 || true
		STARTED_CONTAINER=0
	else
		warn "port publishing failed; retrying with host networking"
	fi

	# With host networking the container's PostgreSQL listens on the loopback
	# port the host can reach.
	"$engine" run --rm -d --name "$CONTAINER" \
		--network host \
		-e POSTGRES_USER="$PG_USER" \
		-e POSTGRES_PASSWORD="$PG_PASSWORD" \
		-e POSTGRES_DB="$PG_DB" \
		"$PG_IMAGE" \
		-c port="$PORT" -c listen_addresses=127.0.0.1 >/dev/null
	STARTED_CONTAINER=1

	if ! wait_for_tcp "$CONTAINER"; then
		"$engine" logs "$CONTAINER" >&2 || true
		die "PostgreSQL did not become ready on 127.0.0.1:$PORT"
	fi
}

start_local() {
	PG_BIN="$(find_local_postgres)" || die "no local PostgreSQL binaries found"
	if (( EUID == 0 )); then
		die "a local PostgreSQL cluster must run as a non-root user; use docker/podman instead"
	fi
	if tcp_ready; then
		die "127.0.0.1:$PORT is already in use; pass --port with a free port"
	fi

	WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/family-budget-pgtest.XXXXXX")"
	log "initializing a temporary PostgreSQL cluster in $WORK_DIR (from $PG_BIN)"
	"$PG_BIN/initdb" -D "$WORK_DIR/data" -U "$PG_USER" --auth=trust --encoding=UTF8 >/dev/null
	"$PG_BIN/pg_ctl" -D "$WORK_DIR/data" -l "$WORK_DIR/postgres.log" \
		-o "-p $PORT -k $WORK_DIR -c listen_addresses=127.0.0.1" -w start >/dev/null
	STARTED_CLUSTER=1
	"$PG_BIN/createdb" -h 127.0.0.1 -p "$PORT" -U "$PG_USER" "$PG_DB"

	if ! wait_for_tcp "local cluster"; then
		cat "$WORK_DIR/postgres.log" >&2 || true
		die "local PostgreSQL did not become ready on 127.0.0.1:$PORT"
	fi
}

if [[ -n "$EXTERNAL_DSN" ]]; then
	DSN="$EXTERNAL_DSN"
	log "using externally provided PostgreSQL DSN (no provisioning)"
else
	RESOLVED_ENGINE="$(resolve_engine)" || die \
		"no database backend available: install docker/podman, install PostgreSQL, or pass --dsn"
	if [[ "$RESOLVED_ENGINE" == local ]]; then
		start_local
	else
		start_container "$RESOLVED_ENGINE"
	fi
	DSN="postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PORT}/${PG_DB}?sslmode=disable"
fi

export TEST_POSTGRES_DSN="$DSN"

cd -- "$REPO_ROOT"

PACKAGES=(./internal/adapters/postgres/...)
if (( RUN_ALL )); then
	PACKAGES=(./...)
fi
GO_ARGS=(-count=1)
if (( RACE )); then
	GO_ARGS+=(-race)
fi

log "running: go test ${GO_ARGS[*]} ${PACKAGES[*]}"
go test "${GO_ARGS[@]}" "${PACKAGES[@]}"
log "integration suite passed"
