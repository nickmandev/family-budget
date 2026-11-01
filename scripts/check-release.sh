#!/usr/bin/env bash
# Periodically checks origin/main for a newer stable release tag and activates
# its prebuilt GHCR images. Intended for the VPS systemd timer, not public HTTP.
set -Eeuo pipefail

repo_dir="${FAMILY_BUDGET_DIR:-/opt/family-budget}"
lock_dir="${HOME:?HOME must be set}/.cache/family-budget"
mkdir -p "$lock_dir"
exec 9>"$lock_dir/release-check.lock"
if ! flock -n 9; then
	printf 'another release check is already running\n'
	exit 0
fi

cd "$repo_dir"
if ! git diff --quiet || ! git diff --cached --quiet; then
	printf 'deployment checkout has tracked local changes; refusing to inspect releases\n' >&2
	exit 1
fi

git fetch --quiet origin +refs/heads/main:refs/remotes/origin/main --tags

tags="$(git tag --merged origin/main --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true)"
if [[ -z "$tags" ]]; then
	printf 'no stable vMAJOR.MINOR.PATCH release tags are reachable from origin/main\n'
	exit 0
fi
latest_tag="$(printf '%s\n' "$tags" | sort -V | tail -n 1)"
latest_version="${latest_tag#v}"
current_version="$(sed -n 's/^APP_VERSION=//p' .env | tail -n 1)"
current_version="${current_version:-1.0.0}"

if [[ ! "$current_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	printf 'APP_VERSION in %s/.env is not a stable version: %s\n' "$repo_dir" "$current_version" >&2
	exit 1
fi
if [[ "$latest_version" == "$current_version" ]]; then
	printf 'already running release %s\n' "$current_version"
	exit 0
fi
newest="$(printf '%s\n%s\n' "$current_version" "$latest_version" | sort -V | tail -n 1)"
if [[ "$newest" != "$latest_version" ]]; then
	printf 'latest repository tag %s is older than deployed release %s; refusing downgrade\n' "$latest_tag" "$current_version" >&2
	exit 1
fi

printf 'deploying new release %s (currently %s)\n' "$latest_tag" "$current_version"
release_dir="$(mktemp -d /tmp/family-budget-release.XXXXXX)"
trap 'rm -rf "$release_dir"' EXIT
git archive "$latest_tag" compose.yaml deploy/postgres/99-small-vps.conf scripts/deploy-release.sh | tar -x -C "$release_dir"
"$release_dir/scripts/deploy-release.sh" "$latest_version" "$release_dir"
