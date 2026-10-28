# PostgreSQL tuning for the small shared VPS

`99-small-vps.conf` is the main PostgreSQL configuration file for the `db`
service. `compose.yaml` mounts it read-only at
`/etc/postgresql/99-small-vps.conf` and starts PostgreSQL with:

```sh
postgres -c config_file=/etc/postgresql/99-small-vps.conf
```

Despite the `99-` prefix it is **not** a `conf.d` drop-in. The official
`postgres:18.6` image generates a `postgresql.conf` without an `include_dir`,
so the file is passed explicitly as `config_file`. Its first directive includes
the initdb-generated file at `/var/lib/postgresql/18/docker/postgresql.conf`,
which preserves image defaults such as `listen_addresses = '*'`; the remaining
lines override only the settings that matter on a constrained host.

## Changing a setting

1. Edit `deploy/postgres/99-small-vps.conf` on the host (keep each override
   above with its one-line rationale).
2. Decide whether the setting is reloadable:
   - **Reloadable** (for example `work_mem`, `maintenance_work_mem`,
     `effective_cache_size`, `checkpoint_timeout`, `random_page_cost`):
     apply without dropping connections:
     ```sh
     docker compose exec db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT pg_reload_conf();"'
     ```
     The bind mount reflects the edited host file immediately; PostgreSQL
     rereads the same `config_file` on reload.
   - **Restart required** (for example `shared_buffers`, `max_connections`,
     `wal_buffers`, `max_wal_size`, `shared_preload_libraries`):
     ```sh
     docker compose restart db
     ```
     This is a clean restart of the container; the `pgdata` volume is reused,
     so no data is lost. `api` reconnects through its pool once `db` passes its
     healthcheck.
3. Verify what the running server actually loaded:
   ```sh
   docker compose exec db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SHOW shared_buffers; SHOW max_connections; SHOW work_mem;"'
   ```
   Compare with `SELECT * FROM pg_settings WHERE source <> '';` if a value does
   not seem to apply (a later `postgresql.auto.conf` entry set with
   `ALTER SYSTEM` wins over this file).

`docker compose up -d db` does not notice edits to the mounted file's
*content* (the Compose service definition is unchanged), so use `restart db`
for restart-only settings rather than expecting `up -d` to recreate it.

## Connection budget

`max_connections = 25` covers:

| Consumer | Connections |
|----------|-------------|
| `api` pool (`DATABASE_MAX_OPEN_CONNS`) | 10 |
| `migrate` (short-lived) | 1 |
| Healthcheck (`pg_isready`) | 1 at a time |
| Operator `psql` / backup | 1–2 |
| Spare | ~10 |

When raising `DATABASE_MAX_OPEN_CONNS`, raise `max_connections` in the same
change and keep at least 5 connections of headroom.

## Upgrading the PostgreSQL major version

The `include` path contains the major version (`18/docker`) because
`postgres:18.6` keeps `PGDATA` at `/var/lib/postgresql/18/docker`. A major
upgrade changes that path; update the first line of `99-small-vps.conf` and the
`pgdata` layout as part of the documented dump/restore upgrade procedure.
