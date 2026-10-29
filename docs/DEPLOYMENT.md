# Deployment — Docker Compose on a Hetzner VPS

Production deployment of the family-budget API. The decisions come from
`PLATFORM_DECISIONS.md` §F and `SECURITY_AND_OPERATIONS.md`.

## Stack overview

```
internet ──> nginx :80/:443 (host, Let's Encrypt)
                  │
                  └─ 127.0.0.1:8080 ──> api :8080 ──> db :5432 (PostgreSQL)
                                             │              │
                                             └── migrate ───┘ (one-shot goose up)
```

| Service   | Image                        | Network(s)        | Host ports | Restart         |
|-----------|------------------------------|-------------------|------------|-----------------|
| `api`     | `family-budget/api:1.0.0`    | `backend`, `frontend` | `127.0.0.1:8080` (loopback only) | `unless-stopped`|
| `migrate` | `family-budget/migrate:1.0.0`| `backend`         | none       | one-shot (`"no"`)|
| `db`      | `postgres:18.6`              | `backend` (internal) | none    | `unless-stopped`|

- `backend` is an `internal: true` Compose network. PostgreSQL is attached only
  to it, publishes no host port, and has no outbound route.
- `frontend` is a non-internal network used only by `api` so the loopback port
  publish is reliable; it carries no other service.
- The host's existing nginx is the sole public listener (80/443) and proxies to
  the API's loopback binding. No proxy runs inside the Compose stack.
- Named volumes: `pgdata` (PostgreSQL data, mounted at
  `/var/lib/postgresql` — PostgreSQL 18's data root). There is no proxy state
  volume; TLS material stays in the host's `/etc/letsencrypt`.

### Image pinning policy

Every image is pinned to an explicit version; `latest` is never used. The
current pins were chosen from the stable releases available at authoring time:

| Pin | Why |
|-----|-----|
| `golang:1.25.14-bookworm` | Builder only; same Go minor as the module (`go 1.25.6`). |
| `alpine:3.24.3` | Minimal runtime; provides CA certificates and busybox `wget` for the API health probe. |
| `postgres:18.6` | Current PostgreSQL 18 stable minor (PLATFORM_DECISIONS §D requires 18.x with an explicit pin). |
| goose `v3.27.3` | Built from source in the Dockerfile (last release compatible with Go 1.25). |

`family-budget/api:1.0.0` and `family-budget/migrate:1.0.0` are local
application release tags built from this repository, not upstream images.

To upgrade: change the pin, run the deployment checks, rebuild, and record the
new version in this table. Read the upstream release notes first; PostgreSQL
major upgrades require a dump/restore, not just a tag bump.

## Prerequisites

- Docker Engine with Compose v2 (`docker compose version`).
- A DNS `A`/`AAAA` record for the chosen subdomain pointing at the VPS.
- nginx and certbot already installed and serving the operator's other projects
  on this host, with Let's Encrypt certificates and renewal as usual.
- Host firewall allowing SSH plus nginx's inbound TCP 80 and 443 (and UDP 443
  only if HTTP/3 is desired).
- Repository checkout on the host. No container binds a public port.

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
   - Optionally set `API_HOST_PORT` (default `8080`); keep it in sync with the
     `proxy_pass` line in the nginx snippet.
   - The public hostname is not app configuration: it lives in the nginx
     `server_name` and in the Let's Encrypt certificate for the subdomain.
   - Keep `.env` out of source control (it is already git-ignored) and never
     copy it into an image.

3. Install the nginx front door and certificate for the subdomain, exactly as
   for the operator's other projects on this host:

   ```sh
   # Replace budget.example.com (CHANGE-ME) in the snippet first.
   sudo cp deploy/nginx/family-budget.conf /etc/nginx/sites-available/
   sudo ln -s /etc/nginx/sites-available/family-budget.conf \
       /etc/nginx/sites-enabled/family-budget.conf
   sudo nginx -t && sudo systemctl reload nginx
   sudo certbot --nginx -d budget.example.com
   ```

   nginx returns 502 until the API is started below; that is expected.

4. Check interpolation before starting anything:

   ```sh
   docker compose config
   ```

5. Build the application images:

   ```sh
   docker compose build
   ```

6. Start PostgreSQL and wait for it to become healthy:

   ```sh
   docker compose up -d db
   docker compose ps
   ```

7. Apply the schema explicitly. The API binary never migrates on startup
   (`cmd/server/main.go`); the reviewed Goose files in `migrations/` are the
   schema authority:

   ```sh
   docker compose run --rm migrate          # goose up
   docker compose run --rm migrate status   # verify applied versions
   ```

8. Start the remaining services:

   ```sh
   docker compose up -d
   ```

   `migrate` runs again as a one-shot service (Goose `up` is idempotent), then
   `api` starts only after it exits successfully and its `/readyz` healthcheck
   passes. nginx proxies to it on the loopback port.

9. Verify:

   ```sh
   docker compose ps
   curl -fsS http://127.0.0.1:8080/readyz          # API directly, loopback
   curl -fsS https://budget.example.com/healthz    # through nginx
   curl -fsS https://budget.example.com/readyz
   curl -sSI https://budget.example.com | grep -i strict-transport-security
   ```

   `/healthz` is liveness (process only). `/readyz` pings PostgreSQL and is the
   probe used by Compose's healthcheck. The HTTPS checks confirm the host nginx
   and its Let's Encrypt certificate are the working front door.

## Routine operations

- Logs: `docker compose logs -f api` (json-file driver, 10 MB × 3 files per
  service). Application logs are structured JSON with request IDs; secrets and
  request bodies are not logged.
- Restart: `docker compose restart api` or `docker compose up -d` after a
  rebuild. The API receives SIGTERM and shuts down gracefully within
  `HTTP_SHUTDOWN_TIMEOUT` (default 10s).
- Front door: hostname/certificate changes are `sudo nginx -t &&
  sudo systemctl reload nginx`. Certificate issuance and renewal stay in the
  host's existing certbot flow; the Compose stack does not manage TLS.
- Migrations: `docker compose run --rm migrate status` to inspect;
  `docker compose run --rm migrate up` to apply. Treat `down` as destructive
  and review the migration first.
- Secret rotation: change `POSTGRES_PASSWORD` and `DATABASE_URL`, then recreate
  the affected services (`docker compose up -d --force-recreate db api`).
  Rotating the PostgreSQL role password inside a running database is a planned
  operational step; do not assume editing `.env` changes it.
- Resource limits (`deploy.resources.limits`): db 0.75 CPU / 768 MiB, api
  0.75 CPU / 256 MiB, migrate 0.25 CPU / 256 MiB. These are ceilings, not
  reservations; see [Resource footprint / sizing](#resource-footprint--sizing).

## Resource footprint / sizing

The stack shares a small Hetzner VPS with the host's existing nginx + Let's
Encrypt, a Calibre instance and other projects. The numbers below assume roughly
**4 vCPU and 8–16 GB RAM** for the whole host, with a meaningful share left for
those other services. They are deliberate **starting points**; adjust after
observing real load (see [What to monitor](#what-to-monitor)).

### Per-service ceilings

| Service | CPU limit | Memory limit | Why |
|---------|-----------|--------------|-----|
| `db` | 0.75 | 768 MiB | 128 MiB `shared_buffers`, up to 3 autovacuum workers with 64 MiB `maintenance_work_mem`, 25 backends and WAL/OS overhead fit under 768 MiB. |
| `api` | 0.75 | 256 MiB | Static Go binary (~20 MiB), bounded request buffers and one 64 MiB Argon2id hash. `GOMEMLIMIT=192MiB` makes the Go GC collect before the container limit. |
| `migrate` | 0.25 | 256 MiB | One-shot Goose run; transient only, exits before `api` starts. |

At full tilt the Compose services can consume at most **1.75 vCPU** and
**1.25 GiB** of RAM, leaving the rest of a 4 vCPU / 8 GB box for nginx, Calibre,
the OS page cache and other projects. In practice they are nowhere near those
ceilings:

- `db`: roughly **250–450 MiB RSS** at steady state for a household-sized
  dataset (128 MiB shared buffers dominate), with short spikes during
  autovacuum or a checkpoint.
- `api`: roughly **25–80 MiB RSS** at idle to light load, with a transient
  ~64 MiB bump per concurrent Argon2id login.
- `migrate`: **0 MiB** at rest; ~20–50 MiB while applying migrations.
- Whole stack at rest: **≈300–550 MiB**, usually under 1 GiB.

CPU is near zero at idle. The limits matter during login bursts (Argon2id) and
PostgreSQL checkpoints/autovacuum. Compose does not enforce
`deploy.resources.reservations` outside Swarm, so only ceilings are declared;
the host scheduler still shares unused CPU fairly.

### PostgreSQL tuning rationale

The authoritative file is [`deploy/postgres/99-small-vps.conf`](../deploy/postgres/99-small-vps.conf)
(mounted read-only and passed as `config_file`; see
[`deploy/postgres/README.md`](../deploy/postgres/README.md) for how to change it
and when a reload is enough versus a restart). Key choices:

- `shared_buffers = 128MB` — enough for this data set without duplicating the
  host page cache or starving other services.
- `effective_cache_size = 512MB` — planner hint, not an allocation; reflects
  the modest page-cache share available to a shared database.
- `work_mem = 4MB`, `maintenance_work_mem = 64MB`, `temp_buffers = 8MB` — keep
  the worst case across 25 connections bounded; a household workload does not
  need large sorts.
- `max_connections = 25` — the API pool is 10, plus migrate, the healthcheck,
  host-level maintenance and an operator session, with headroom.
- `wal_buffers = 4MB`, `min_wal_size = 128MB`, `max_wal_size = 1GB`,
  `checkpoint_timeout = 15min` — low traffic means fewer, larger checkpoints;
  this reduces fsync bursts and bounds WAL on a small disk.
- `random_page_cost = 1.1` — SSD storage; favours indexes for small selective
  queries.
- `max_parallel_workers = 2`, `jit = off` — prevent the database from using all
  host cores or paying JIT compilation cost for tiny queries.

The API pool is bounded explicitly by `internal/config` defaults (open 10, idle
5, connection lifetime 1h, idle time 15m) and exposed as
`DATABASE_MAX_OPEN_CONNS`, `DATABASE_MAX_IDLE_CONNS`,
`DATABASE_CONN_MAX_LIFETIME` and `DATABASE_CONN_MAX_IDLE_TIME`. Keep the open
limit comfortably below `max_connections`; see the connection budget in
`deploy/postgres/README.md`.

### What to monitor

- **Host memory and swap** (`free -h`, `vmstat 1`): available memory should stay
  well above ~15%; sustained swap-in/out means the host is overcommitted.
- **Container memory** (`docker stats --no-stream`): watch `db` and `api` against
  their limits. `docker inspect` shows `OOMKilled` and the configured limit.
- **Disk** (`df -h`, `df -i`): PostgreSQL WAL, the `pgdata` volume, Docker
  images/logs and other host data all share the VPS disk.
- **PostgreSQL** (`pg_stat_activity`, slow query log if enabled,
  `SHOW max_connections`): connection saturation or repeated temp-file spills
  are signals to revisit `work_mem` or the pool.
- **API** (`/readyz`, structured logs): latency and restart counts.

Alerting wiring for these signals is in [`RUNBOOK.md`](RUNBOOK.md) §1. If the
host starts swapping or OOM-killing containers, use the
[degraded host / OOM snippet](RUNBOOK.md#degraded-host--oom) rather than simply
raising limits on a box that has no memory to spare.

## Security posture

- Only the host nginx listens publicly (80/443). The API is published on
  `127.0.0.1:8080` (loopback) so only the host nginx can reach it; PostgreSQL's
  5432 is reachable only from the internal `backend` network.
- Containers run as non-root: the API and migrate images use numeric UID
  10001. `migrate` needs no writable filesystem.
- Secrets live only in the git-ignored `.env` (mode 600) and never in the
  Compose file, image layers or build context (`.dockerignore` excludes
  `.env*`). Only `db` receives `POSTGRES_PASSWORD`; only `api`/`migrate`
  receive `DATABASE_URL`; no proxy container receives database credentials.
- nginx terminates TLS with the operator's existing Let's Encrypt certificate
  and sets HSTS, `X-Content-Type-Options`, `X-Frame-Options`,
  `Referrer-Policy` and `Cross-Origin-Opener-Policy` (same header set as the
  earlier reverse-proxy proposal).
- Keep the host patched, use key-based SSH and a host firewall. Rebuild images
  after base-image security updates.

## Static checks

`deploy/` contains Go tests that statically validate the Dockerfile, Compose
file, nginx snippet and `.env.example` (pinned images, multi-stage build,
non-root user, private database, loopback-only API, ordered migrations, log
rotation, named volumes). They are guards, not container builds:

```sh
go test ./deploy/...
```

They run in `go test ./...`. Actual image build and stack startup must be
validated in CI or on the deployment host, where a Docker daemon is available.

## Operations

Day-to-day monitoring and incident procedures live in
[`RUNBOOK.md`](RUNBOOK.md). The runbook covers alerting for host memory/swap,
disk and `/readyz` health, and the degraded-host/OOM, database-startup and
data-consistency incident snippets.

Database backups are managed outside this repository by the owner's existing
infrastructure; this repository ships no backup/restore tooling. Schema changes
are applied with the reviewed Goose files, and the integration runner validates
the persistence layer against a disposable PostgreSQL:

```sh
scripts/integration-test.sh --all --race
```

- **Monitoring and alerting:** see [`RUNBOOK.md`](RUNBOOK.md) §1. Low disk,
  host memory pressure, container OOM kills and `/readyz` checks are covered
  there.
- **Incidents:** see [`RUNBOOK.md`](RUNBOOK.md) §2 for degraded-host/OOM,
  database-startup/corruption and data-consistency guidance.
- **Staging:** still open. Until a staging environment exists, use an isolated
  directory/stack on the VPS (or a fresh VPS) for validation.
