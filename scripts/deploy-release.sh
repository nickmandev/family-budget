#!/usr/bin/env bash
# Pull and activate a versioned GHCR release on the VPS. The caller is the
# systemd release poller, which verifies the tag is on origin/main first.
set -Eeuo pipefail

version="${1:-}"
release_files="${2:-}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	printf 'invalid application version: %s\n' "$version" >&2
	exit 2
fi
if [[ -z "$release_files" ]]; then
	printf 'release configuration directory is required\n' >&2
	exit 2
fi

deploy_dir="${FAMILY_BUDGET_DIR:-/opt/family-budget}"
cd "$deploy_dir"
if [[ ! -f .env || ! -f compose.yaml || ! -f deploy/postgres/99-small-vps.conf ]]; then
	printf 'release deployment requires .env, compose.yaml, and the PostgreSQL tuning file in %s\n' "$deploy_dir" >&2
	exit 1
fi
for required in "$release_files/compose.yaml" "$release_files/deploy/postgres/99-small-vps.conf"; do
	if [[ ! -f "$required" ]]; then
		printf 'tagged release configuration is missing: %s\n' "$required" >&2
		exit 1
	fi
done

previous_version="$(sed -n 's/^APP_VERSION=//p' .env | tail -n 1)"
previous_version="${previous_version:-1.0.0}"
backup_env="$(mktemp /tmp/family-budget-env.XXXXXX)"
backup_compose="$(mktemp /tmp/family-budget-compose.XXXXXX)"
backup_postgres="$(mktemp /tmp/family-budget-postgres.XXXXXX)"
cp .env "$backup_env"
cp compose.yaml "$backup_compose"
cp deploy/postgres/99-small-vps.conf "$backup_postgres"
changed=0
env_tmp=""

cleanup() {
	result=$?
	if (( result != 0 && changed )); then
		printf 'release failed; restoring the previous app version/configuration (%s) and restarting its API image\n' "$previous_version" >&2
		cp "$backup_env" .env
		cp "$backup_compose" compose.yaml
		cp "$backup_postgres" deploy/postgres/99-small-vps.conf
		docker compose up -d --no-deps --force-recreate api || printf 'WARNING: previous API image restart failed\n' >&2
	fi
	if [[ -n "$env_tmp" ]]; then
		rm -f "$env_tmp"
	fi
	rm -f "$backup_env" "$backup_compose" "$backup_postgres"
	exit "$result"
}
trap cleanup EXIT

env_tmp="$(mktemp "$deploy_dir/.env.XXXXXX")"
awk -v version="$version" '
	BEGIN { found = 0 }
	/^APP_VERSION=/ {
		if (!found) print "APP_VERSION=" version
		found = 1
		next
	}
	{ print }
	END {
		if (!found) print "APP_VERSION=" version
	}
' .env > "$env_tmp"
chmod 0600 "$env_tmp"
mv "$env_tmp" .env
env_tmp=""
changed=1
install -m 0644 "$release_files/compose.yaml" compose.yaml
install -m 0644 "$release_files/deploy/postgres/99-small-vps.conf" deploy/postgres/99-small-vps.conf

docker compose config >/dev/null
docker compose pull api migrate
docker compose run --rm migrate
docker compose up -d --no-deps --force-recreate api

healthy=0
for attempt in $(seq 1 45); do
	container_id="$(docker compose ps -q api || true)"
	if [[ -n "$container_id" ]]; then
		health_status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container_id" 2>/dev/null || true)"
		if [[ "$health_status" == "healthy" ]]; then
			healthy=1
			break
		fi
	fi
	sleep 2
done

if (( healthy == 0 )); then
	docker compose logs --tail=80 api >&2 || true
	printf 'API did not become ready after release %s\n' "$version" >&2
	exit 1
fi

printf 'family-budget release %s is healthy\n' "$version"
