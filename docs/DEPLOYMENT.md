# Deployment — Docker Compose on a Hetzner VPS

Production deployment of the family-budget API. The decisions come from
`PLATFORM_DECISIONS.md` §F and `SECURITY_AND_OPERATIONS.md`.

## Stack overview

```
internet ──> caddy :80/:443 ──> api :8080 ──> db :5432 (PostgreSQL)
                                  │              │
                                  └── migrate ───┘ (one-shot goose up)
```

| Service   | Image                        | Network(s)        | Host ports | Restart         |
|-----------|------------------------------|-------------------|------------|-----------------|
| `caddy`   | `caddy:2.11.7`               | `frontend`        | 80, 443     | `unless-stopped`|
| `api`     | `family-budget/api:1.0.0`    | `backend`, `frontend` | none    | `unless-stopped`|
| `migrate` | `family-budget/migrate:1.0.0`| `backend`         | none       | one-shot (`"no"`)|
| `db`      | `postgres:18.6`              | `backend` (internal) | none    | `unless-stopped`|

- `backend` is an `internal: true` Compose network. PostgreSQL is attached only
  to it, publishes no host port, and has no outbound route.
- `frontend` carries only Caddy ↔ API traffic.
- Named volumes: `pgdata` (PostgreSQL data, mounted at
  `/var/lib/postgresql` — PostgreSQL 18's data root), `caddy_data` and
  `caddy_config` (certificates and Caddy state).

### Image pinning policy

Every image is pinned to an explicit version; `latest` is never used. The
current pins were chosen from the stable releases available at authoring time:

| Pin | Why |
|-----|-----|
| `golang:1.25.14-bookworm` | Builder only; same Go minor as the module (`go 1.25.6`). |
| `alpine:3.24.3` | Minimal runtime; provides CA certificates and busybox `wget` for the API health probe. |
| `postgres:18.6` | Current PostgreSQL 18 stable minor (PLATFORM_DECISIONS §D requires 18.x with an explicit pin). |
| `caddy:2.11.7` | Current Caddy 2.x stable; automatic TLS. |
| goose `v3.27.3` | Built from source in the Dockerfile (last release compatible with Go 1.25). |

`family-budget/api:1.0.0` and `family-budget/migrate:1.0.0` are local
application release tags built from this repository, not upstream images.

To upgrade: change the pin, run the deployment checks, rebuild, and record the
new version in this table. Read the upstream release notes first; PostgreSQL
major upgrades require a dump/restore, not just a tag bump.

## Prerequisites

- Docker Engine with Compose v2 (`docker compose version`).
- A DNS `A`/`AAAA` record for `DOMAIN` pointing at the VPS.
- Host firewall allowing SSH plus inbound TCP 80 and 443 (and UDP 443 only if
  HTTP/3 is desired).
- Repository checkout on the host and permission to bind ports 80/443.

## First run

1. Create the secret environment file and restrict it:

   ```sh
   cp .env.example .env
   chmod 600 .env
   ```

2. Edit `.env`:
   - Set `POSTGRES_PASSWORD` to a long random value, e.g.
     `openssl rand -base64 32`.
   - Set the same password inside `DATABASE_URL`, percent-encoding URL-special
     characters (`@` → `%40`, `:` → `%3A`, `/` → `%2F`, `#` → `%23`).
   - Set `DOMAIN` to the real hostname and `ACME_EMAIL` to the certificate
     contact address.
   - Keep `.env` out of source control (it is already git-ignored) and never
     copy it into an image.

3. Check interpolation before starting anything:

   ```sh
   docker compose config
   ```

4. Build the application images:

   ```sh
   docker compose build
   ```

5. Start PostgreSQL and wait for it to become healthy:

   ```sh
   docker compose up -d db
   docker compose ps
   ```

6. Apply the schema explicitly. The API binary never migrates on startup
   (`cmd/server/main.go`); the reviewed Goose files in `migrations/` are the
   schema authority:

   ```sh
   docker compose run --rm migrate          # goose up
   docker compose run --rm migrate status   # verify applied versions
   ```

7. Start the remaining services:

   ```sh
   docker compose up -d
   ```

   `migrate` runs again as a one-shot service (Goose `up` is idempotent), then
   `api` starts only after it exits successfully, and Caddy starts only after
   `/readyz` succeeds.

8. Verify:

   ```sh
   docker compose ps
   curl -fsS https://"$DOMAIN"/healthz
   curl -fsS https://"$DOMAIN"/readyz
   curl -sSI https://"$DOMAIN" | grep -i strict-transport-security
   ```

   `/healthz` is liveness (process only). `/readyz` pings PostgreSQL and is the
   probe used by Compose and by Caddy's dependency ordering.

## Routine operations

- Logs: `docker compose logs -f api` (json-file driver, 10 MB × 3 files per
  service). Application logs are structured JSON with request IDs; secrets and
  request bodies are not logged.
- Restart: `docker compose restart api` or `docker compose up -d` after a
  rebuild. Caddy and the API receive SIGTERM and shut down gracefully within
  `HTTP_SHUTDOWN_TIMEOUT` (default 10s).
- Migrations: `docker compose run --rm migrate status` to inspect;
  `docker compose run --rm migrate up` to apply. Treat `down` as destructive
  and review the migration first.
- Secret rotation: change `POSTGRES_PASSWORD` and `DATABASE_URL`, then recreate
  the affected services (`docker compose up -d --force-recreate db api`).
  Rotating the PostgreSQL role password inside a running database is a planned
  operational step; do not assume editing `.env` changes it.
- Resource limits (`deploy.resources.limits`): db 1 CPU / 1 GB, api 1 CPU /
  512 MB, migrate 0.5 CPU / 256 MB, caddy 0.5 CPU / 128 MB. Adjust for the VPS
  size and watch for OOM kills.

## Security posture

- Only Caddy publishes host ports (80/443). PostgreSQL's 5432 is reachable
  only from the internal `backend` network.
- Containers run as non-root: the API and migrate images use numeric UID
  10001. `migrate` needs no writable filesystem.
- Secrets live only in the git-ignored `.env` (mode 600) and never in the
  Compose file, image layers or build context (`.dockerignore` excludes
  `.env*`). Only `db` receives `POSTGRES_PASSWORD`; only `api`/`migrate`
  receive `DATABASE_URL`; Caddy receives neither.
- Caddy provisions TLS automatically and sets HSTS, `X-Content-Type-Options`,
  `X-Frame-Options`, `Referrer-Policy` and `Cross-Origin-Opener-Policy`.
- Keep the host patched, use key-based SSH and a host firewall. Rebuild images
  after base-image security updates.

## Static checks

`deploy/` contains Go tests that statically validate the Dockerfile, Compose
file, Caddyfile and `.env.example` (pinned images, multi-stage build, non-root
user, private database, ordered migrations, log rotation, named volumes).
They are guards, not container builds:

```sh
go test ./deploy/...
```

They run in `go test ./...`. Actual image build and stack startup must be
validated in CI or on the deployment host, where a Docker daemon is available.

## Operations

Day-to-day backup, restore, retention, alerting and incident procedures live
in [`RUNBOOK.md`](RUNBOOK.md). That runbook records the selected defaults:
`pg_dump -Fc` inside the `db` container, `age` encryption before transfer,
rsync over SSH to a Hetzner Storage Box, 6-hourly cadence, count-based
retention, proposed RPO/RTO and the mandatory restore drill.

The supporting scripts are:

```sh
scripts/backup.sh --help           # encrypted dump + off-host upload
scripts/restore.sh --help          # clean restore, migrations, API start
scripts/verify-restore.sh --help   # connectivity/schema/consistency/readiness
```

- **Backups and recovery:** see [`RUNBOOK.md`](RUNBOOK.md) §1–§4. The TODO list
  that used to live here (target, schedule, retention, key custody, RPO/RTO,
  restore rehearsal, backup alerting) is resolved there.
- **Monitoring and alerting:** see [`RUNBOOK.md`](RUNBOOK.md) §5. Backup
  failure, stale-backup age, low disk and `/readyz` checks are covered by the
  proposed healthchecks.io + systemd timer setup.
- **Staging:** still open. Until a staging environment exists, use an isolated
  directory/stack on the VPS (or a fresh VPS) for restore drills, as described
  in the runbook.
