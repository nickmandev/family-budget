# Deployment — Docker Compose on a Hetzner VPS

Production deployment of the family-budget API. The decisions come from
`PLATFORM_DECISIONS.md` §F and `SECURITY_AND_OPERATIONS.md`.

## Stack overview

```
internet ──> Cloudflare (proxied DNS, edge TLS, bot protection)
                  │  Full (strict)
                  ▼
          nginx :80/:443 (host, Let's Encrypt)
                  │
                  └─ 127.0.0.1:8080 ──> api :8080 ──> db :5432 (PostgreSQL)
                                             │              │
                                             └── migrate ───┘ (one-shot goose up)
```

| Service   | Image                        | Network(s)        | Host ports | Restart         |
|-----------|------------------------------|-------------------|------------|-----------------|
| `api`     | `family-budget/api:${APP_VERSION}`    | `backend`, `frontend` | `127.0.0.1:8080` (loopback only) | `unless-stopped`|
| `migrate` | `family-budget/migrate:${APP_VERSION}`| `backend`         | none       | one-shot (`"no"`)|
| `db`      | `postgres:18.6`              | `backend` (internal) | none    | `unless-stopped`|

- `backend` is an `internal: true` Compose network. PostgreSQL is attached only
  to it, publishes no host port, and has no outbound route.
- `frontend` is a non-internal network used only by `api` so the loopback port
  publish is reliable; it carries no other service.
- The host's existing nginx is the sole public listener (80/443) and proxies to
  the API's loopback binding. Cloudflare is the only public entry point in front
  of it; no proxy runs inside the Compose stack.
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

`family-budget/api:<version>` and `family-budget/migrate:<version>` are local
application release tags built from this repository, not upstream images.
`APP_VERSION` selects the matching pair; it defaults to `1.0.0` for existing
installations and is updated by the release deployment workflow.

To upgrade: change the pin, run the deployment checks, rebuild, and record the
new version in this table. Read the upstream release notes first; PostgreSQL
major upgrades require a dump/restore, not just a tag bump.

## Prerequisites

- Docker Engine with Compose v2 (`docker compose version`) on the VPS. The dev
  PC that builds images also needs Docker with Compose v2.
- A Cloudflare-managed DNS zone for the domain, with a **proxied** `A`/`AAAA`
  record for `budget-api.<domain>` pointing at the VPS (see
  [Cloudflare](#cloudflare)).
- nginx and certbot already installed and serving the operator's other projects
  on this host, with Let's Encrypt certificates and renewal as usual.
- Host firewall allowing SSH, plus inbound TCP 80 and 443 **only from the
  Cloudflare ranges** (and UDP 443 only if HTTP/3 is desired).
- For bootstrap/manual recovery, the repository and deployment files are synced
  to the host with `rsync`; images are built off-host, not on the memory-limited
  VPS. For regular version releases, the GitHub Actions workflow builds and
  transfers the versioned images. No container binds a public port.

## Cloudflare

The public hostname is `budget-api.<domain>`; Cloudflare sits in front of the
VPS:

```
client ──TLS──> Cloudflare ──Full (strict)──> nginx :443 ──> 127.0.0.1:8080
```

- **DNS:** an `A` (and optionally `AAAA`) record for `budget-api.<domain>`
  pointing at the VPS with proxy status **proxied** (orange cloud). A grey-cloud
  record bypasses Cloudflare, so the real-client-IP restoration never runs.
- **SSL/TLS mode:** **Full (strict)**, never **Flexible**. The origin already
  serves a valid Let's Encrypt certificate; Flexible would send plaintext to the
  origin and silently break origin TLS.
- **Always Use HTTPS:** enabled, so Cloudflare upgrades HTTP before it reaches
  the origin.
- **Bot protection** is Cloudflare's job at the edge. The application still
  enforces authentication and per-IP rate limits itself; Cloudflare is not a
  substitute for either.
- **Origin firewall:** allow `:80`/`:443` only from Cloudflare's published ranges
  (`https://www.cloudflare.com/ips-v4` and `/ips-v6`). This is what makes
  `CF-Connecting-IP` trustworthy — a client that can reach the origin directly
  could spoof the header.
- **Real-IP dependency:** the rate limiter and request log use the client IP
  from `X-Forwarded-For`, trusting only loopback peers
  (`HTTP_TRUSTED_PROXIES`, default `127.0.0.0/8,::1/128`). The nginx snippet
  restores the real address with `set_real_ip_from` + `real_ip_header
  CF-Connecting-IP`; keep that range list in sync with Cloudflare's published
  ranges. Without it every user would share the Cloudflare edge's bucket.

The drop-in server block is
[`deploy/nginx/family-budget.conf`](../deploy/nginx/family-budget.conf); its
[README](../deploy/nginx/README.md) has the short install guide.

## Database connection configuration

The database connection is configured with separate components, never a
connection URL. `.env.example` defines each value once and compose passes the
same variable names to the services that consume them.

| Variable | Example | Consumed by |
|----------|---------|-------------|
| `POSTGRES_HOST` | `db` (the compose service name) | `api`, `migrate` |
| `POSTGRES_PORT` | `5432` | `api`, `migrate` |
| `POSTGRES_USER` | `family_budget` | `db`, `api`, `migrate` |
| `POSTGRES_PASSWORD` | long random value | `db`, `api`, `migrate` |
| `POSTGRES_DB` | `family_budget` | `db`, `api`, `migrate` |
| `POSTGRES_SSLMODE` | `disable` on the private network | `api`, `migrate` |

### HTTP and frontend configuration

| Variable | Example | Consumed by |
|----------|---------|-------------|
| `HTTP_CORS_ALLOWED_ORIGINS` | `https://budget.nickmandev.com` (comma-separated exact origins; empty disables CORS) | `api` |

#### Frontend / CORS

The browser frontend origin is `https://budget.nickmandev.com`. The API's CORS
allowlist permits that exact origin; browser preflights are answered before
authentication and rate limiting. CORS is not authorization: protected API
routes still require a bearer token. Preflights allow the `Authorization`,
`Content-Type`, `Idempotency-Key` and `X-Request-ID` request headers. To add
another frontend origin, add it to
`HTTP_CORS_ALLOWED_ORIGINS` as a comma-separated exact origin and restart the
`api` service.

`internal/config` constructs the `postgres://` URL internally with proper
percent-encoding and the selected `sslmode`; `cmd/migrate` calls the same helper,
so server, migrations and tests can never disagree. **Never construct or paste a
connection URL yourself** — there is no URL environment variable.

The connection-pool variables (`DATABASE_MAX_OPEN_CONNS`,
`DATABASE_MAX_IDLE_CONNS`, `DATABASE_CONN_MAX_LIFETIME`,
`DATABASE_CONN_MAX_IDLE_TIME`) are separate and documented in `.env.example`.

## First run

Images are built on the dev PC (memory-rich) and shipped to the VPS; the VPS
never runs a build. The end-to-end sequence is environment components → rsync →
ship → migrate → up → nginx/certbot → Cloudflare DNS → smoke test.

1. Prepare the environment components on the dev PC. `.env` is git-ignored and
   never goes into an image; the rsync in step 2 carries it to the VPS (or
   create it directly on the VPS afterwards):

   ```sh
   cp .env.example .env
   chmod 600 .env
   ```

   Edit `.env`:
   - Set `POSTGRES_PASSWORD` to a long random value, e.g.
     `openssl rand -base64 32`. The same value is read by `db`, `api` and
     `migrate`; do not copy it into a URL and do not percent-encode it.
   - Leave `POSTGRES_HOST=db`, `POSTGRES_PORT=5432`, `POSTGRES_USER`,
     `POSTGRES_DB` and `POSTGRES_SSLMODE=disable` as shipped unless the
     database lives outside the compose stack (then set `POSTGRES_HOST` and the
     other components accordingly).
   - Optionally set `API_HOST_PORT` (default `8080`); keep it in sync with the
     `proxy_pass` line in the nginx snippet.
   - Leave `HTTP_TRUSTED_PROXIES` at its loopback default unless another
     trusted proxy sits in front of nginx.
   - The public hostname is not app configuration: it lives in the nginx
     `server_name`, the Let's Encrypt certificate and Cloudflare DNS.
   - Keep `.env` out of source control and never copy it into an image.

2. Sync the repository from the dev PC. The ship script does not copy files, and
   the VPS must have the same `compose.yaml`, `deploy/` and `migrations/` as the
   images you are about to ship:

   ```sh
   # On the dev PC, in the repository root:
   rsync -a --exclude .git ./ <ssh-host>:/opt/family-budget/
   ```

3. Build and ship the application images from the dev PC:

   ```sh
   scripts/ship-image.sh --host <ssh-host>
   ```

   This runs `docker compose build` locally, then `docker save` → `scp` →
   `docker load` on the VPS. Use `--load-only` to skip the rebuild, `--dry-run`
   to inspect the commands, and `--keep-tarball` to keep the transfer file. The
   upstream `postgres:18.6` image is not shipped; pull it on the VPS if it is
   missing (`docker compose pull db`).

4. Check interpolation on the VPS before starting anything:

   ```sh
   cd /opt/family-budget
   docker compose config
   ```

5. Apply the schema explicitly. The API binary never migrates on startup
   (`cmd/server/main.go`); the reviewed Goose files in `migrations/` are the
   schema authority:

   ```sh
   docker compose run --rm migrate          # goose up
   docker compose run --rm migrate status   # verify applied versions
   ```

6. Start the stack:

   ```sh
   docker compose up -d
   docker compose ps
   ```

   `migrate` runs again as a one-shot service (Goose `up` is idempotent), then
   `api` starts only after it exits successfully and its `/readyz` healthcheck
   passes. nginx proxies to it on the loopback port.

7. Install the nginx front door and certificate for the subdomain, exactly as
   for the operator's other projects on this host:

   ```sh
   # Replace budget-api.<domain> (CHANGE-ME) in the snippet first.
   sudo cp deploy/nginx/family-budget.conf /etc/nginx/sites-available/
   sudo ln -s /etc/nginx/sites-available/family-budget.conf \
       /etc/nginx/sites-enabled/family-budget.conf
   sudo nginx -t && sudo systemctl reload nginx
   sudo certbot --nginx -d budget-api.<domain>
   ```

   nginx returns 502 until the API is running. If HTTP-01 validation through
   the Cloudflare proxy fails, temporarily grey-cloud the record for issuance
   and switch it back to proxied afterwards.

8. Configure Cloudflare (see [Cloudflare](#cloudflare)): proxy the
   `budget-api.<domain>` record to the VPS, set SSL mode **Full (strict)**, turn
   on **Always Use HTTPS**, and restrict origin `:80`/`:443` to Cloudflare's
   ranges in the host firewall.

9. Smoke-test through the public hostname:

   ```sh
   curl -fsS http://127.0.0.1:8080/readyz              # API directly, loopback
   curl -fsS https://budget-api.<domain>/healthz       # through Cloudflare + nginx
   curl -fsS https://budget-api.<domain>/readyz
   curl -sSI https://budget-api.<domain> | grep -i strict-transport-security
   ```

   `/healthz` is liveness (process only). `/readyz` pings PostgreSQL and is the
   probe used by Compose's healthcheck. The HTTPS checks confirm Cloudflare,
   the host nginx and its Let's Encrypt certificate are the working front door.

## Routine operations

- Logs: `docker compose logs -f api` (json-file driver, 10 MB × 3 files per
  service). Application logs are structured JSON with request IDs; secrets and
  request bodies are not logged.
- Restart: `docker compose restart api` or `docker compose up -d` after a new
  image has been shipped with `scripts/ship-image.sh`. The API receives SIGTERM
  and shuts down gracefully within `HTTP_SHUTDOWN_TIMEOUT` (default 10s).
- Front door: hostname/certificate changes are `sudo nginx -t &&
  sudo systemctl reload nginx`. Certificate issuance and renewal stay in the
  host's existing certbot flow; the Compose stack does not manage TLS.
- Migrations: `docker compose run --rm migrate status` to inspect;
  `docker compose run --rm migrate up` to apply. Treat `down` as destructive
  and review the migration first.
- Secret rotation: change `POSTGRES_PASSWORD` (and any other `POSTGRES_*`
  component if the database moved), then recreate the affected services
  (`docker compose up -d --force-recreate db api migrate`). The connection URL
  is rebuilt internally from the components. Rotating the PostgreSQL role
  password inside a running database is a planned operational step; do not
  assume editing `.env` changes it.
- Resource limits (`deploy.resources.limits`): db 0.75 CPU / 768 MiB, api
  0.75 CPU / 256 MiB, migrate 0.25 CPU / 256 MiB. These are ceilings, not
  reservations; see [Resource footprint / sizing](#resource-footprint--sizing).

## Automated release deployment

The workflow in `.github/workflows/ci.yml` runs the normal verification suite on
pull requests and `main` pushes. When a `vMAJOR.MINOR.PATCH` tag is pushed to a
commit already on `main`, it runs verification and publishes versioned API and
migration images to GitHub Container Registry (GHCR). It does **not** connect to
the VPS or require a Hetzner SSH secret.

The VPS-side systemd timer polls the existing clone for new stable tags reachable
from `origin/main`. When it finds a version newer than `APP_VERSION` in `.env`,
it pulls the matching GHCR images, applies migrations, recreates the API, and
waits for the Compose health check. This keeps the deployment credentials and
restart action on the VPS instead of allowing a GitHub runner to SSH into it.

### GHCR setup

The release job publishes these two packages using the workflow's
`GITHUB_TOKEN` with `packages: write` permission:

- `ghcr.io/nickmandev/family-budget-api:<version>`
- `ghcr.io/nickmandev/family-budget-migrate:<version>`

Set the package visibility in GitHub to **public** for unauthenticated pulls, or
log in to GHCR on the VPS as the same deployment user that runs the timer using a
read-only `read:packages` token. For a private package, keep that token in the
deployment user's Docker credential store; never put it in `.env`, Compose, the
repository, or the image. Confirm the resulting image package names match
`IMAGE_REGISTRY` and `IMAGE_OWNER` in the VPS `.env`.

### Install the VPS release poller

The cloned repository must be at `/opt/family-budget`, have an `origin` remote
that can fetch `main` and tags, and be writable by the deployment user so Git
can update its refs. That user must be able to update `.env`, access the Docker
socket, and pull the GHCR packages. The timer checks every five minutes; no
inbound webhook or additional public port is needed. The poller does not check
out or reset the clone: it archives the selected release's deployment files
into a temporary directory and installs those files only after the release
image is available.

Install the systemd templates and enable the timer for the repository/deployment
user (replace `family-budget-deploy` with the account that owns the clone):

```sh
sudo install -m 0644 deploy/systemd/family-budget-release-check@.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/family-budget-release-check@.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now family-budget-release-check@family-budget-deploy.timer
systemctl list-timers 'family-budget-release-check@*'
```

Ensure that user belongs to the `docker` group (which is effectively
root-equivalent) and can fetch the repository. For private GHCR packages, log in
as that user, using a read-only `read:packages` token:

```sh
echo "$GHCR_READ_TOKEN" | docker login ghcr.io -u nickmandev --password-stdin
```

Do not put the token in shell history, `.env`, or a unit file. Prefer public
GHCR packages if consistent with your distribution choice. Inspect poller logs
with:

```sh
sudo journalctl -u family-budget-release-check@family-budget-deploy.service
```

Restrict creation of `v*` release tags to trusted maintainers. The timer checks
that a tag is reachable from `origin/main`, refuses downgrades, and serializes
its own runs with a lock. It refuses to run if the cloned checkout has tracked
local changes; keep production configuration in the ignored `.env` rather than
editing tracked Compose or script files on the VPS.

### Releasing

Create a stable semantic-version tag on a commit already merged to `main` and
push it:

```sh
git tag -a v1.2.3 -m "Release v1.2.3"
git push origin v1.2.3
```

The workflow maps `v1.2.3` to the GHCR image tag and the poller updates only
`APP_VERSION` in the VPS `.env`. `.env.example` defaults it to `1.0.0` for
existing installations. The deploy script restores the previous app version and
restarts the prior image if pulling, migration, or readiness fails. It does **not**
reverse a database migration. Use backward-compatible expand/contract migrations
and never rely on automatic schema rollback.

### Release smoke check

To verify the delivery path without changing API behavior, a documentation-only
commit can be merged to `main` and tagged with the next version. Confirm the tag
run publishes both GHCR packages, then allow the VPS timer one polling interval
(five minutes). On the VPS, verify the `APP_VERSION` update, API container
creation time, and `/readyz` response:

```sh
grep '^APP_VERSION=' /opt/family-budget/.env
cd /opt/family-budget && docker compose ps
curl -fsS http://127.0.0.1:8080/readyz
```

The tag must be greater than the currently deployed `APP_VERSION`; the poller
intentionally refuses equal or older releases. For example, if the current
version is `0.1.0`, use `v0.1.1` for a smoke check.

Manual recovery remains available with `scripts/ship-image.sh`; set
`APP_VERSION` to the same version in the VPS `.env` before running Compose with
the shipped images. `scripts/check-release.sh` and `scripts/deploy-release.sh`
are intended to run through the systemd timer, not as public endpoints. The
poller uses `git archive` from the selected tag, so the deployed Compose and
deployment script correspond to the same tagged source as the container images.

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
  `.env*`). `db`, `api` and `migrate` receive the database components they need;
  the `api` and `migrate` binaries construct the connection URL internally. No
  proxy container receives database credentials.
- Cloudflare is the only public entry point. Its origin firewall allows `:80`
  and `:443` only from the published Cloudflare ranges, which is also what makes
  `CF-Connecting-IP` (and therefore the restored real client IP used for rate
  limiting) trustworthy. Only the loopback peer is a trusted proxy
  (`HTTP_TRUSTED_PROXIES`).
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
rotation, named volumes, Cloudflare real-IP restoration) plus the
`scripts/ship-image.sh` guard (no hardcoded host or credentials, `--help` runs
nothing). They are guards, not container builds:

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

Household timezones require the IANA database. The server binary embeds it via
`time/tzdata`, and the Alpine runtime additionally installs `tzdata`, so
`time.LoadLocation` works in the container. A missing database surfaces as
`INVALID_VALUE` when creating a household with a non-UTC timezone.

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
