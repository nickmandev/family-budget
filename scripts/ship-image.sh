#!/usr/bin/env bash
#
# Build the compose images on this machine and ship them to the VPS.
#
# The VPS is memory constrained, so images are built here (the dev PC) and
# transferred with `docker save` over scp followed by `docker load`. The
# repository is not copied: rsync compose.yaml, .env and the migrations
# separately. SSH uses the caller's existing SSH config/agent; this script
# stores no keys and no passwords.
#
# Usage: scripts/ship-image.sh --host <ssh-host> [OPTIONS]

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." >/dev/null 2>&1 && pwd)"

compose_env_value() {
	local name="$1"
	if [[ -f "$REPO_ROOT/.env" ]]; then
		sed -n "s/^${name}=//p" "$REPO_ROOT/.env" | tail -n 1
	fi
}

# Application images referenced by compose.yaml. postgres is an upstream image
# and is pulled on the VPS, not shipped from here. Explicit exported values win;
# otherwise use the local Compose .env if present.
APP_VERSION="${APP_VERSION:-$(compose_env_value APP_VERSION)}"
APP_VERSION="${APP_VERSION:-1.0.0}"
[[ "$APP_VERSION" =~ ^[A-Za-z0-9_.-]+$ ]] || {
	printf '[ship] ERROR: APP_VERSION contains characters that are not valid in a Docker tag\n' >&2
	exit 1
}
IMAGE_REGISTRY="${IMAGE_REGISTRY:-$(compose_env_value IMAGE_REGISTRY)}"
IMAGE_REGISTRY="${IMAGE_REGISTRY:-ghcr.io}"
IMAGE_OWNER="${IMAGE_OWNER:-$(compose_env_value IMAGE_OWNER)}"
IMAGE_OWNER="${IMAGE_OWNER:-nickmandev}"
API_IMAGE="${API_IMAGE:-$IMAGE_REGISTRY/$IMAGE_OWNER/family-budget-api:$APP_VERSION}"
MIGRATE_IMAGE="${MIGRATE_IMAGE:-$IMAGE_REGISTRY/$IMAGE_OWNER/family-budget-migrate:$APP_VERSION}"

log() { printf '[ship] %s\n' "$*" >&2; }
die() {
	printf '[ship] ERROR: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
Usage: scripts/ship-image.sh --host <ssh-host> [OPTIONS]

Build the compose images locally and load them into the VPS Docker daemon.
Images are never built on the VPS (it is memory constrained): this script runs
\`docker compose build\` here, then \`docker save\`, \`scp\`, and \`docker load\` on
the remote host. SSH uses your existing SSH config/agent; no key or password is
stored in this script.

The repository is NOT copied. Sync it first when compose.yaml, .env or the
migrations change (use your SSH host alias for <ssh-host>):

  rsync -a --exclude .git ./ <ssh-host>:/opt/family-budget/

Options:
  --host HOST         SSH destination for ssh/scp, normally an alias from your
                      SSH config (required).
  --remote-dir DIR    Directory on the VPS holding compose.yaml
                      (default: /opt/family-budget).
  --load-only         Skip the local rebuild and ship the images that already
                      exist locally.
  --keep-tarball      Keep the image tarball locally and on the VPS.
  --dry-run           Print the commands instead of running them.
  -h, --help          Show this help and exit.

Images shipped:
  $API_IMAGE
  $MIGRATE_IMAGE
EOF
}

HOST=""
REMOTE_DIR="/opt/family-budget"
LOAD_ONLY=0
KEEP_TARBALL=0
DRY_RUN=0

while [[ $# -gt 0 ]]; do
	case "$1" in
	--host)
		[[ $# -ge 2 ]] || die "--host requires a value"
		HOST="$2"
		shift
		;;
	--remote-dir)
		[[ $# -ge 2 ]] || die "--remote-dir requires a value"
		REMOTE_DIR="$2"
		shift
		;;
	--load-only) LOAD_ONLY=1 ;;
	--keep-tarball) KEEP_TARBALL=1 ;;
	--dry-run) DRY_RUN=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown argument: $1 (see --help)" ;;
	esac
	shift
done

[[ -n "$HOST" ]] || die "--host is required (see --help)"
[[ -n "$REMOTE_DIR" ]] || die "--remote-dir must not be empty"

if (( ! DRY_RUN )); then
	command -v docker >/dev/null 2>&1 || die "docker not found in PATH"
	command -v ssh >/dev/null 2>&1 || die "ssh not found in PATH"
	command -v scp >/dev/null 2>&1 || die "scp not found in PATH"
fi

TARBALL="$(mktemp "${TMPDIR:-/tmp}/family-budget-images.XXXXXX")"
REMOTE_TARBALL="${REMOTE_DIR%/}/family-budget-images.tar"

cleanup() {
	local status=$?
	if (( KEEP_TARBALL == 0 )) && [[ -f "$TARBALL" ]]; then
		rm -f -- "$TARBALL"
	fi
	exit "$status"
}
trap cleanup EXIT

cd -- "$REPO_ROOT"

if (( LOAD_ONLY )); then
	log "skipping local build (--load-only)"
else
	log "building compose images locally"
	if (( DRY_RUN )); then
		printf '[ship] DRY-RUN: docker compose build\n' >&2
	else
		docker compose build
	fi
fi

log "saving images: $API_IMAGE $MIGRATE_IMAGE"
if (( DRY_RUN )); then
	printf '[ship] DRY-RUN: docker save -o %q %q %q\n' \
		"$TARBALL" "$API_IMAGE" "$MIGRATE_IMAGE" >&2
else
	docker save -o "$TARBALL" "$API_IMAGE" "$MIGRATE_IMAGE"
fi

log "copying image tarball to $HOST:$REMOTE_TARBALL"
if (( DRY_RUN )); then
	printf '[ship] DRY-RUN: ssh %q mkdir -p %q\n' "$HOST" "$REMOTE_DIR" >&2
	printf '[ship] DRY-RUN: scp %q %q\n' "$TARBALL" "$HOST:$REMOTE_TARBALL" >&2
	printf '[ship] DRY-RUN: ssh %q docker load -i %q\n' "$HOST" "$REMOTE_TARBALL" >&2
else
	ssh "$HOST" mkdir -p "$REMOTE_DIR"
	scp "$TARBALL" "$HOST:$REMOTE_TARBALL"
	ssh "$HOST" docker load -i "$REMOTE_TARBALL"
fi

if (( KEEP_TARBALL == 0 )); then
	if (( DRY_RUN )); then
		printf '[ship] DRY-RUN: ssh %q rm -f %q\n' "$HOST" "$REMOTE_TARBALL" >&2
	else
		ssh "$HOST" rm -f -- "$REMOTE_TARBALL"
	fi
fi

log "image ship to $HOST complete"

cat >&2 <<EOF

Next steps on the VPS (compose.yaml and .env must already be in $REMOTE_DIR):
  ssh $HOST 'cd $REMOTE_DIR && docker compose up -d'
  ssh $HOST 'cd $REMOTE_DIR && docker compose run --rm migrate'   # after schema changes

If the repository is not synced yet, copy it first (this script does not):
  rsync -a --exclude .git ./ $HOST:$REMOTE_DIR/
EOF
