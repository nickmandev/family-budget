# Operations Runbook — Monitoring, Incidents and Recovery

This runbook is the operational companion to
[`SECURITY_AND_OPERATIONS.md`](SECURITY_AND_OPERATIONS.md) and
[`PLATFORM_DECISIONS.md`](PLATFORM_DECISIONS.md) §F. It covers monitoring,
alerting and incident response for a **personal** Hetzner VPS deployment. The
canonical specifications are left unchanged; the corresponding decisions are
recorded in [`IMPLEMENTATION_DECISIONS.md`](IMPLEMENTATION_DECISIONS.md).

> **Out of scope: database backups.** Database backups are managed outside this
> repository by the owner's existing infrastructure. This repository's tooling
> stops at migrations and the integration runner; it ships no backup/restore
> scripts and no backup policy, encryption or retention procedure.

The operational script in [`scripts/`](../scripts) is:

| Script | Purpose |
|--------|---------|
| `scripts/integration-test.sh` | Disposable PostgreSQL (Docker/Podman/local `initdb`) + full persistence integration suite. |

The integration runner has been executed live; see
[§3](#3-quality-gate-evidence).

## 1. Alerting requirements (proposal)

A running container is not enough; these signals must reach a human:

| Signal | Why | Proposed low-maintenance mechanism |
|--------|-----|-------------------------------------|
| Low disk space | PostgreSQL and WAL stop accepting writes | Daily systemd timer checks `df` and pings only when free space drops below 15% (or on error); include `df -i` if inode pressure is a risk. |
| Host memory pressure | OOM killer / swap thrash affects every project on the shared box | Daily (or 5-minute) timer checks `free`/`vmstat` and alerts when available memory is below 15% or the host is swapping steadily. |
| Container OOM kill | A service restarts without an obvious application error | Check `docker inspect` for `OOMKilled`/exit 137 and `docker stats`; alert on unexpected restarts of `db`/`api`. |
| Service health | API/DB outage or restart loop | External HTTP check on `https://budget.example.com/readyz` every 5 minutes. |
| Owner-managed backup monitoring | Backup health is not handled in this repository | The owner's existing host-level backup infrastructure reports success, failure and freshness. |

**Selected default:** [healthchecks.io](https://healthchecks.io/) free tier
(≈20 checks, email/webhook alerts, **€0**) for the daily disk check, plus a
separate external uptime check for `/readyz`. It is outside the VPS failure
domain, which a self-hosted monitor on the same box is not. A self-hosted
Uptime Kuma is a viable free alternative if the operator already runs external
monitoring.

Example cron entry (adjust the ping UUID and path):

```cron
# Daily disk check: succeed silently, alert when free space is below 15%.
15 3 * * * cd /opt/family-budget && \
  test "$(df -P / | awk 'NR==2 {gsub("%","",$5); print 100-$5}')" -ge 15 && \
  curl -fsS -m 10 https://hc-ping.com/REPLACE-DISK-UUID >/dev/null || \
  curl -fsS -m 10 https://hc-ping.com/REPLACE-DISK-UUID/fail >/dev/null
```

The healthcheck period and grace are configured on the provider, not in this
repository. Logs contain the summary line and errors only; no passwords, keys or
financial payloads.

### Host-level checks for the shared box

This VPS also runs nginx, a Calibre instance and other projects, so a healthy
container can still be a symptom of an unhealthy host. Use these before and
after changing limits (see also [DEPLOYMENT.md § Resource footprint /
sizing](DEPLOYMENT.md#resource-footprint--sizing)):

```sh
free -h                              # available RAM and swap
vmstat 1 5                           # si/so columns: sustained swap activity is bad
docker stats --no-stream             # live per-container CPU/memory against limits
df -h / && df -i /                   # disk blocks and inodes
docker compose ps                    # restart/health state
```

For a specific container, confirm whether it hit its own ceiling or the host
ran out of memory:

```sh
docker inspect --format '{{.Name}} OOMKilled={{.State.OOMKilled}} exit={{.State.ExitCode}} limit={{.HostConfig.Memory}}' \
  $(docker compose ps -q)
sudo dmesg -T | grep -iE 'oom|killed process' | tail
journalctl -k --since '1 hour ago' | grep -iE 'oom|killed process'
```

## 2. Incident snippets

### Degraded host / OOM

Symptoms: `api` or `db` restarts, 502s from nginx, SSH/login slow, or the host
becomes unresponsive while the application itself logged nothing obvious.

1. **Stop and check the host before restarting anything.** Run the host-level
   checks above. If available memory is near zero and the host is swapping, a
   restart can make it worse; free memory first (stop a non-essential service
   such as Calibre, or a runaway process).
2. **Distinguish container limit from host pressure.** `docker inspect` shows
   `OOMKilled=true`/exit 137. If `db`/`api` were killed *below* their configured
   limits, the host was under pressure; if they hit the limit, the service needs
   tuning or a justified limit increase. `dmesg`/`journalctl -k` confirm a
   host-level OOM kill.
3. **Check the API runtime first.** `GOMEMLIMIT` must stay below the `api`
   memory limit (currently 192 MiB under 256 MiB). A `GOMEMLIMIT` above the
   container limit lets the Go heap grow until the kernel kills the container.
4. **Check PostgreSQL next.** Confirm the running values:
   ```sh
   docker compose exec db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SHOW shared_buffers; SHOW work_mem; SHOW maintenance_work_mem; SHOW max_connections;"'
   ```
   If `db` is the pressure point, lower (in order of effect)
   `shared_buffers`, `maintenance_work_mem`, `work_mem`, and `max_connections`
   (raising the API pool only makes this worse). Apply reloadable settings with
   `SELECT pg_reload_conf();`; `shared_buffers` and `max_connections` need
   `docker compose restart db`. See `deploy/postgres/README.md`.
5. **Recover in the right order:** database healthy → `migrate` completed →
   `api` healthy → `curl -fsS http://127.0.0.1:8080/readyz` → nginx reload/TLS.
   Do not `docker compose down -v`; the `pgdata` volume is the database.
6. **Only raise limits if the host has headroom.** The shipped ceilings
   (db 768 MiB, api 256 MiB) already leave most of a 4 vCPU / 8 GB box free.
   Raising them on a host that is swapping just moves the failure around.
7. **When to move `db` to its own box:** sustained host memory pressure or swap
   despite tuning, database growth beyond a few GB, disk I/O from the database
   affecting the other projects, or a need for more than ~1 GB of database
   memory. A separate Hetzner instance (or managed PostgreSQL) is the clean
   boundary; restoring from the owner's host-level backup into a fresh target is
   outside this repository's tooling. Until then, prefer tuning down over
   expanding on the shared host.

### `db` will not start or corruption is suspected

1. Do **not** run `docker compose down -v` and do not delete the `pgdata`
   volume; preserve it for diagnosis.
2. Capture `docker compose logs db` and `docker compose ps`.
3. If the database is reachable, take an immediate host-level backup using the
   owner's backup infrastructure (outside this repository).
4. If it is not reachable, treat the latest verified host-level backup as the
   recovery point and restore it on a clean host (or into a fresh database on
   the same host) before touching the original volume.
5. Only after the restored instance is verified should the operator decide
   whether to keep the old volume for forensics.

### Data looks wrong (bad migration, projection drift, accidental edits)

A non-zero `ledger_version` mismatch count or an unexpected row count is a
signal to restore the owner's host-level backup to an **isolated** stack,
compare, and only then cut over. The full `VerifyProjections` balance rebuild
is a Go integration-test facility (`internal/adapters/postgres`), not part of
this runbook; run it against the restored database when deeper evidence is
needed.

### Lost password or credential exposure

Rotate `POSTGRES_PASSWORD`/`DATABASE_URL` and recreate `db`/`api`/`migrate`.
Restore from a known-good host-level backup onto a **clean** host rather than
trusting the compromised one.

## 3. Quality gate evidence

`MVP_SCOPE.md` quality gate 3 requires PostgreSQL integration tests for
migrations, constraints, idempotency, rollback and concurrent
spending/reallocation. A passing run is now recorded, not assumed.

Recorded live run:

- Date: 2026-10-28.
- PostgreSQL: 18.6 (`postgres:18.6`, the `compose.yaml` pin).
- Provisioning: throwaway rootless Docker 29.9.0 container with host networking
  (the rootless pasta network could not publish ports; the runner detects this
  and falls back automatically). Reproduce with
  `scripts/integration-test.sh --all --race`.
- PostgreSQL adapter suite: 47 test cases (19 top-level + 28 subtests), 47
  passed, 0 failed, 0 skipped.
- Full `go test -count=1 ./...` with `TEST_POSTGRES_DSN` set: 1193 test cases
  (359 top-level), all passed, 0 failed, 0 skipped.
- `go test -count=1 -race ./...` with `TEST_POSTGRES_DSN` set: all packages
  green.

The first live run also found four test-harness assumptions that had never been
executed (migration SQL splitting, the `ON DELETE RESTRICT` SQLSTATE, the
migration count, and non-hexadecimal test fingerprints); they were fixed in the
test code. Future gate-3 evidence should record the same fields: date,
PostgreSQL version, and passed/failed/skipped counts.
