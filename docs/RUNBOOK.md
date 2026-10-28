# Operations Runbook — Backups, Restore and Recovery

This runbook is the operational companion to
[`SECURITY_AND_OPERATIONS.md`](SECURITY_AND_OPERATIONS.md) and
[`PLATFORM_DECISIONS.md`](PLATFORM_DECISIONS.md) §F. It resolves the open
deployment choices for a **personal** Hetzner VPS deployment with concrete,
low-maintenance proposals. The canonical specifications are left unchanged;
the corresponding decisions are recorded in
[`IMPLEMENTATION_DECISIONS.md`](IMPLEMENTATION_DECISIONS.md).

The scripts are in [`scripts/`](../scripts):

| Script | Purpose |
|--------|---------|
| `scripts/backup.sh` | `pg_dump -Fc` inside the `db` container, sha256, `age` encryption, rsync to the off-host target, retention. |
| `scripts/restore.sh` | Verify + decrypt + restore into a **fresh** database, then migrate, start the API and verify. |
| `scripts/verify-restore.sh` | Connectivity, schema, row-count, ledger consistency and `/readyz` checks. |

> The scripts are statically validated in `go test ./deploy/...` and with
> `bash -n`. They have **not** been executed against a live Docker daemon in the
> repository's CI. The first backup and the first restore drill must happen on
> the deployment host and be recorded in the drill log below.

## 1. Backup policy

### Method (database-aware, non-negotiable)

The primary artifact is `pg_dump --format=custom` (`-Fc`) executed **inside**
the `db` container against the running database:

```sh
docker compose exec -T db pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom
```

A raw copy of the `pgdata` volume, `docker cp` of `/var/lib/postgresql` or a
`tar` of PGDATA is **not** a backup and must never be the primary artifact. The
custom format lets `pg_restore` restore selectively and in parallel later.

Each run writes three files named after the UTC timestamp:

```text
family-budget_20261028T060000Z.dump.age        # age-encrypted custom-format dump
family-budget_20261028T060000Z.dump.age.sha256 # sha256 of the encrypted file
family-budget_20261028T060000Z.meta            # plaintext hash, size, DB name
```

The encrypted file is checksummed before upload, so a truncated or altered
transfer is detected before a restore is attempted.

### Cadence (proposal)

**Every 6 hours** (00:00, 06:00, 12:00, 18:00 UTC) via a systemd timer or cron.
This yields a worst-case **RPO of 6 hours**.

Why 6 hours and not continuous archiving: a household budget is written to by
a handful of people at human pace. Six hours bounds the loss to "transactions
entered since the last run" without running WAL archiving, a second database,
or a paid continuous-backup service. If the household starts using the instance
as the system of record for every purchase, tighten the timer to hourly (the
script is unchanged; only the schedule and `BACKUP_KEEP_REMOTE` change).

### Retention (proposal)

| Tier | Where | Kept | Mechanism |
|------|-------|------|-----------|
| Operational | Off-host target | **7 days** of 6-hourly backups (28 artifacts) | `BACKUP_KEEP_REMOTE=28` |
| Monthly archive | Off-host target `archive/YYYY-MM/` | **6 monthly** artifacts | documented monthly copy step below |
| Local staging | VPS `./backups/` | **8 artifacts** (≈2 days) | `BACKUP_KEEP_LOCAL=8` |

The local copy is only a staging area for the upload and a convenience for a
fast same-host restore; it is not a backup of record because it shares the
VPS failure domain. Seven days of operational history is enough to notice an
accidental deletion or a bad schema change and restore before the evidence is
pruned. Six monthly artifacts cover slow-to-notice corruption. Weekly artifacts
are unnecessary because the 6-hourly set already contains them.

Monthly archive step (run on the 1st, or add to cron). It copies the newest
operational artifact to `archive/YYYY-MM/` and prunes archives older than six
months:

```sh
set -euo pipefail
# shellcheck source=/dev/null
. ./.env
monthly_dir="archive/$(date -u +%Y-%m)"
ssh -p "${BACKUP_SSH_PORT}" -i "${BACKUP_SSH_KEY}" "${BACKUP_SSH_TARGET}" \
  "mkdir -p '${BACKUP_REMOTE_PATH}/${monthly_dir}' && \
   cp -p '${BACKUP_REMOTE_PATH}'/\$(ls -1t '${BACKUP_REMOTE_PATH}'/family-budget_*.dump.age | head -n1) \
         '${BACKUP_REMOTE_PATH}/${monthly_dir}/' && \
   ls -1dt '${BACKUP_REMOTE_PATH}'/archive/*/ | tail -n +7 | xargs -r rm -rf --"
```

### Off-host target (proposal): Hetzner Storage Box

**Selected default:** a Hetzner Storage Box reached with **rsync over SSH**
(port 23; the Storage Box SSH/rsync port). It fits the existing Hetzner VPS
deployment, is a separate failure domain (a different Hetzner product with its
own storage), and is inexpensive and low-maintenance. Key-based SSH only; do
not enable password login.

- `BACKUP_SSH_TARGET=u123456@u123456.your-storagebox.de`
- `BACKUP_SSH_PORT=23`
- `BACKUP_SSH_KEY=/root/.ssh/storagebox_ed25519`
- `BACKUP_REMOTE_PATH=family-budget`

**Estimated cost:** the smallest Storage Box (BX11, 1 TB) is roughly
**€3.81/month at the time of writing** (check current Hetzner pricing and VAT).
A personal budget database and its 6-hourly dumps are well under 1 GB total, so
the smallest plan is already far larger than needed. The script uses plain
rsync, not the Storage Box snapshot feature; snapshots are an optional extra
safety net.

### Alternative (documented, not implemented): S3-compatible object storage

An S3-compatible bucket is a reasonable substitute if the operator already pays
for object storage. The scripts currently implement rsync/SSH only; switching
means replacing the upload/prune steps with `aws s3 cp` / `rclone` and lifecycle
rules. Server-side lifecycle policies are a good fit for the retention tiers.

| Provider | Approximate cost for <1 GB of encrypted dumps | Notes |
|----------|-----------------------------------------------|-------|
| Hetzner Object Storage | ≈ €4.99/month for the smallest plan (1 TB) | Same vendor, separate product/failure domain. |
| Backblaze B2 | ≈ $0.006/GB/month → **well under $0.10/month** for this data | Free egress up to 3× stored volume; minimum charges negligible. |
| MinIO (self-hosted) | €0 in software, but runs on the VPS | **Not** a separate failure domain unless run on another host; not recommended as the only target. |

Estimates are for a database plus 28–120 small artifacts, not for the plan's
full capacity. Re-check pricing before committing.

## 2. Encryption and key custody

### Selected tool: `age`

Backups are encrypted with [`age`](https://age-encryption.org/) before they
leave the VPS. `age` was chosen over OpenSSL because:

- it is a single static binary with no configuration and no dependency chain;
- it uses modern primitives (X25519 + ChaCha20-Poly1305) with a small,
  auditable format;
- **public-key encryption** means the VPS only holds a public recipient, so
  compromising the VPS does not reveal the decryption key;
- key generation, encryption and decryption are one command each, which keeps
  the runbook short.

OpenSSL with a passphrase would put a shared secret on the VPS and require
passphrase handling in every script; it is not used.

### Keypair scheme

Generate the keypair **on the operator's machine, never on the VPS**:

```sh
mkdir -p ~/.config/age
age-keygen -o ~/.config/age/family-budget.key
# prints: Public key: age1...
chmod 600 ~/.config/age/family-budget.key
```

- **Public key** (`age1...`) → `BACKUP_ENCRYPTION_PUBLIC_KEY` in the VPS `.env`
  (mode 600). It can encrypt but not decrypt, so its presence on the VPS is
  safe.
- **Private identity** (`AGE-SECRET-KEY-1...`) → the operator's machine only,
  referenced by `BACKUP_AGE_IDENTITY_FILE` for restore. It is **never** copied
  to the VPS, the images, the repository or the off-host backup target.

The private key must be backed up out-of-band. Recommended low-maintenance
custody:

1. Password manager entry (primary copy).
2. An encrypted/offline copy in a second safe place (for example an encrypted
   USB stick or a printed recovery key in a safe). Two independent copies
   protects against device loss without spreading the key around.

**Loss of the private key makes every backup unrecoverable.** There is no
recovery path; the ciphertext is useless without the identity. This is the
central trade-off of client-side encryption and is why the escrow copies above
are mandatory before the first production backup.

### Rotation

1. Generate a new keypair on the operator machine.
2. Update `BACKUP_ENCRYPTION_PUBLIC_KEY` in `.env`; keep the old private
   identity available until all artifacts encrypted to the old key have aged
   out of retention.
3. Take a fresh backup and verify it restores with the **new** identity in a
   drill.
4. Update the escrow copies and retire the old identity only after the last
   old-key artifact is gone.

The scripts encrypt to a single recipient. If dual-recipient encryption is
wanted during a rotation window, encrypt to both keys manually or add a
multi-recipient mode in a reviewed change.

## 3. RPO and RTO (proposals)

| Objective | Proposal | Rationale |
|-----------|----------|-----------|
| **RPO** (max data loss) | **6 hours** | Matches the 6-hourly cadence. A personal budget is updated at human pace; losing up to half a day of entries is recoverable manually and does not justify continuous WAL archiving. Tighten to 1 hour if daily business use warrants it. |
| **RTO** (time to restore) | **30–60 minutes** for a personal instance | Restore is a fresh database plus `pg_restore`, migrations and verification on the same VPS, or a fresh VPS with the Compose stack. The lower end assumes the VPS and images are intact and the operator has the age identity; the upper end includes provisioning a clean VPS, DNS/TLS and spot checks. |

These are **targets until the first drill**. A drill that takes longer must
update this table rather than the other way round. Track the measured RPO
(age of the artifact used) and RTO (wall-clock from "go" to verified API) in
the drill log.

## 4. Restore drill procedure (quality gate 6)

The point of a drill is to prove that an **encrypted off-host** artifact
restores into a **clean** database/host and serves traffic. Run it before the
first production launch, then **quarterly** and after any change to the backup
scripts, Compose topology or PostgreSQL major version.

### Required secrets and commands

- The `age` **private identity** on the operator machine
  (`BACKUP_AGE_IDENTITY_FILE`).
- SSH key for the Storage Box (for `--fetch-latest`).
- Repository checkout and the `.env` of the target (new database password if
  the drill host is fresh).
- `docker`, `docker compose`, `age`, `sha256sum`, `rsync`, `ssh` on the host.

### Checklist

1. **Schedule a window** and note the start time. Prefer a fresh VPS or a
   separate directory/stack so production is untouched.
2. **Provision a clean stack:** clone the repository, `cp .env.example .env`,
   set a **new** `POSTGRES_PASSWORD`/`DATABASE_URL` and `DOMAIN`, `chmod 600
   .env`, then `docker compose config` and `docker compose build`.
3. **Start only PostgreSQL:** `docker compose up -d db` and wait for healthy
   (`docker compose ps`).
4. **Get the artifact:** either copy it from the Storage Box or let the script
   fetch the newest one with `--fetch-latest`. Keep the private identity on the
   operator machine; only the public key is in the target `.env`.
5. **Restore:**
   ```sh
   scripts/restore.sh --fetch-latest --force --require-data
   # or: scripts/restore.sh ./backups/family-budget_YYYYmmddTHHMMSSZ.dump.age --force --require-data
   ```
   This verifies the sha256, decrypts, drops/recreates the database, runs
   `pg_restore`, applies `goose up`, starts `api` and calls
   `verify-restore.sh --require-data`.
6. **Verification must pass:** `pg_isready`, `SELECT 1`, expected tables, non-zero
   row counts, `ledger_version` equal to the journal operation count, and
   `/readyz` returning ready. Paste the counts into the drill log.
7. **Application spot checks:** log in, load the household, open a recent
   transaction, compare a known account/envelope balance and the Ready-to-assign
   figure with the pre-incident values. Reload and confirm the values persist.
8. **Cutover (only when replacing production):**
   - Stop writes on the old host: `docker compose stop api`.
   - Take one final backup of the old host if it is still readable.
   - If staying on the same VPS: `scripts/restore.sh` already recreates the
     database and starts `api`; confirm `caddy` is up and serves HTTPS.
   - If moving hosts: point DNS at the new VPS (or move the stack), keep the
     `caddy_data` volume or let Caddy re-issue the certificate, then
     `docker compose up -d`.
   - Verify `https://$DOMAIN/healthz`, `/readyz`, HSTS and the UI/API.
   - Keep the old host read-only for 24–72 hours, then decommission it.
9. **Record the drill** in the log template and fix anything that surprised
   you. A failed drill is a successful finding; do not mark it passed.

### Drill log template

Copy this block per drill (keep it in the private ops notes, not in the repo if
it contains hostnames or timing that should stay private):

```text
Drill date (UTC):
Operator:
Environment (fresh VPS / same VPS / isolated directory):
Artifact restored (filename, created_at):
Artifact age at restore (measured RPO):
Backup retention tier used:
Start / end time (measured RTO):
Row counts (households / journal_operations / account_postings /
            envelope_postings / idempotency_records):
ledger_version check: pass/fail
/readyz + UI/API spot checks: pass/fail
Issues found:
Follow-up actions + owner:
RPO/RTO table updated? (yes/no)
```

### Drill cadence (proposal)

- Before the first production launch (mandatory, quality gate 6).
- Every quarter.
- After changes to `scripts/`, `compose.yaml`, the PostgreSQL major version or
  the off-host target.

## 5. Alerting requirements (proposal)

A green backup command is not enough; these signals must reach a human:

| Signal | Why | Proposed low-maintenance mechanism |
|--------|-----|-------------------------------------|
| Backup failure | No new recovery point | systemd `OnFailure=` unit or cron `||` branch pings the failure URL; also `MAILTO` for cron. |
| Stale backup age | The timer silently stopped | Dead-man's-switch check (for example healthchecks.io) with a 6-hour period and a 1-hour grace; no ping means an alert. |
| Low disk space | PostgreSQL and WAL stop accepting writes | Daily systemd timer checks `df` and pings only when free space drops below 15% (or on error). |
| Service health | API/DB outage or restart loop | External HTTP check on `https://$DOMAIN/readyz` every 5 minutes. |

**Selected default:** [healthchecks.io](https://healthchecks.io/) free tier
(≈20 checks, email/webhook alerts, **€0**) for backup success/failure, stale
age and the daily disk check, plus a separate external uptime check for
`/readyz`. It is outside the VPS failure domain, which a self-hosted monitor on
the same box is not. A self-hosted Uptime Kuma is a viable free alternative if
the operator already runs external monitoring, but it does not replace the
dead-man's-switch for backups.

Example cron entry (adjust the ping UUID and path):

```cron
# 6-hourly encrypted backup; ping healthchecks.io on success, failure URL on error.
0 */6 * * * cd /opt/family-budget && \
  ./scripts/backup.sh >>/var/log/family-budget-backup.log 2>&1 && \
  curl -fsS -m 10 https://hc-ping.com/REPLACE-UUID >/dev/null || \
  curl -fsS -m 10 https://hc-ping.com/REPLACE-UUID/fail >/dev/null

# Daily disk check: succeed silently, alert when free space is below 15%.
15 3 * * * cd /opt/family-budget && \
  test "$(df -P / | awk 'NR==2 {gsub("%","",$5); print 100-$5}')" -ge 15 && \
  curl -fsS -m 10 https://hc-ping.com/REPLACE-DISK-UUID >/dev/null || \
  curl -fsS -m 10 https://hc-ping.com/REPLACE-DISK-UUID/fail >/dev/null
```

The healthcheck period and grace are configured on the provider, not in the
scripts. Logs contain the summary line and errors only; no passwords, keys or
financial payloads.

## 6. Incident snippets

### `db` will not start or corruption is suspected

1. Do **not** run `docker compose down -v` and do not delete the `pgdata`
   volume; preserve it for diagnosis.
2. Capture `docker compose logs db` and `docker compose ps`.
3. If the database is reachable, take an immediate backup
   (`scripts/backup.sh --no-upload` at minimum, ideally with upload).
4. If it is not reachable, treat the latest verified off-host artifact as the
   recovery point and follow the restore drill on a clean host (or into a fresh
   database on the same host) before touching the original volume.
5. Only after the restored instance is verified should the operator decide
   whether to keep the old volume for forensics.

### Data looks wrong (bad migration, projection drift, accidental edits)

`scripts/verify-restore.sh` is safe to run against a live database: it only
reads. A non-zero `ledger_version` mismatch count or an unexpected row count is
a signal to restore a known-good artifact to an **isolated** stack first,
compare, and only then cut over. The full `VerifyProjections` balance rebuild
is a Go integration-test facility (`internal/adapters/postgres`), not part of
this SQL-only check; run it against the restored database when deeper evidence
is needed.

### Lost encryption key

The backups are unrecoverable without the private identity. This is by design.
Mitigations: check the password manager and the second escrow copy; a lost key
is not a lost **database** if the VPS is intact. If the VPS is healthy:
generate a new keypair immediately, update the public key in `.env`, take a
fresh backup, verify it, and rotate escrow copies. Old artifacts encrypted to
the lost key remain unreadable and should be pruned once the new chain is
verified.

### Lost password or credential exposure

Rotate `POSTGRES_PASSWORD`/`DATABASE_URL` and recreate `db`/`api`/`migrate`;
rotate the Storage Box SSH key and the age keypair if the VPS may be
compromised. Restore from a known-good artifact onto a **clean** host rather
than trusting the compromised one.

## 7. Installing the schedule (example systemd units)

Place these on the VPS under `/etc/systemd/system/` and enable the timer. The
service runs as root only because it needs the Docker socket; keep the
repository and `.env` mode 600 and owned by the deployment user.

```ini
# /etc/systemd/system/family-budget-backup.service
[Unit]
Description=family-budget encrypted off-host backup
After=docker.service
Requires=docker.service
OnFailure=family-budget-alert@%n.service

[Service]
Type=oneshot
WorkingDirectory=/opt/family-budget
EnvironmentFile=/opt/family-budget/.env
ExecStart=/opt/family-budget/scripts/backup.sh
```

```ini
# /etc/systemd/system/family-budget-backup.timer
[Unit]
Description=Run the family-budget backup every 6 hours

[Timer]
OnCalendar=*-*-* 00,06,12,18:00:00 UTC
Persistent=true
RandomizedDelaySec=5m

[Install]
WantedBy=timers.target
```

```sh
systemctl daemon-reload
systemctl enable --now family-budget-backup.timer
systemctl list-timers family-budget-backup.timer
```

After enabling, run `scripts/backup.sh` manually once and confirm the summary
line, the off-host artifact and a `scripts/restore.sh` drill. Installation is
not complete until the first drill passes.
