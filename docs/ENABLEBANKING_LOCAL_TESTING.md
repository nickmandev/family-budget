# Enable Banking local sandbox testing

Use this runbook to verify the phase 1 consent round-trip against the Enable
Banking sandbox before merging. This uses localhost for both the provider
callback and the browser result page; it does not expose the API publicly.

## Prerequisites

- An Enable Banking sandbox application with
  `http://localhost:8080/callbacks/enablebanking` listed among its allowed
  redirect URLs.
- The **unencrypted private key exported for that application**. It must match
  the certificate registered with Enable Banking; do not generate a replacement
  key for an existing application.
- Docker Engine with Docker Compose v2.
- An owner account and household in the local database for the smoke script.

## Setup

From the repository root, put the exported key in the ignored `secrets/`
directory and create the local environment file:

```sh
mkdir -p secrets
cp /path/to/exported-private-key.pem secrets/enablebanking.key
chmod 644 secrets/enablebanking.key
openssl rand -base64 32
cp .env.local.example .env.local
chmod 600 .env.local
```

The container runs as UID 10001; a `0600` key owned by your host user is unreadable.

Edit `.env.local` and fill in the sandbox application UUID in
`ENABLEBANKING_APPLICATION_ID` and the output of `openssl rand -base64 32` in
`ENABLEBANKING_ENCRYPTION_KEY`. The private key stays in `secrets/`; do not put
it in the environment file or commit either local file. The template uses
household currency EUR and Enable Banking country BG.

Set the API smoke credentials and household ID in your shell (or pass the
email and household as script flags). The script reads the password only from
`BANK_SMOKE_PASSWORD`:

```sh
export BANK_SMOKE_EMAIL='owner@example.com'
export BANK_SMOKE_PASSWORD='your-local-account-password'
export BANK_SMOKE_HOUSEHOLD='<household-uuid>'
```

## Start the local stack

```sh
docker compose --env-file .env.local up -d db
docker compose --env-file .env.local run --rm migrate
docker compose --env-file .env.local up -d api worker
curl -sf 127.0.0.1:8080/readyz
```

The API is published only on loopback. The `worker` has no host port; its
heartbeat is checked below.

## Consent flow

1. List sandbox banks and find the exact ASPSP name:

   ```sh
   scripts/bank-smoke.sh aspsps
   ```

2. Start a consent attempt using the exact name returned above:

   ```sh
   scripts/bank-smoke.sh connect --aspsp-name "<exact name>"
   ```

   The script prints the connection ID and provider `redirect_url`, but never
   prints the API session token.

3. Open the printed redirect URL in a browser and complete the sandbox SCA flow
   for the ASPSP you selected (for example **Mock ASPSP** or Genome). The set of
   sandbox ASPSPs is curated by Enable Banking and changes over time; Fibank and
   other live banks may only appear for a production application. The Mock ASPSP
   is the most deterministic option: configure its mock accounts/transactions
   in the Enable Banking Control Panel first, then the consent screen returns to
   this API's callback like any other ASPSP. On success, the browser should return to
   `http://localhost:4200/bank/callback?status=completed`.

   The frontend branch `feat/enablebanking-fe-phase1` (owned by another agent)
   supplies `/bank/callback` and can be run with `npm start`. Until it is
   available, set `ENABLEBANKING_RESULT_URL=` in `.env.local` and recreate the
   API; the callback then returns the outcome as JSON instead of redirecting to
   the frontend.

## Verify the result

This exact flow has been verified locally end to end with Mock ASPSP (connection reaches `AUTHORIZED`).

Check the connection status and worker heartbeat:

```sh
scripts/bank-smoke.sh status
docker compose --env-file .env.local exec db psql -U family_budget -d family_budget \
  -c "select id, status, session_expires_at, session_id_encrypted from bank_connections;"
docker compose --env-file .env.local exec db psql -U family_budget -d family_budget \
  -c "select state_hash from bank_connections;"
docker compose --env-file .env.local exec db psql -U family_budget -d family_budget \
  -c "select worker_name, last_tick_at from worker_heartbeats;"
```

The connection should show `AUTHORIZED` and a session expiry. The persisted
`session_id_encrypted` must be ciphertext, not the provider session ID, and each
`state_hash` must be a 64-character hexadecimal SHA-256 digest. When Enable
Banking is enabled, `worker_heartbeats.last_tick_at` should be populated after
the worker's scheduled pass.

Check logs for sensitive values:

```sh
docker compose --env-file .env.local logs api worker | grep -Ei 'code=|state=|BEGIN|PRIVATE KEY|eyJ'
```

No callback code, raw state, private key material, or JWT-like value should be
logged. Any match should be inspected and treated as unsafe unless it is clearly
an unrelated, non-sensitive value. An Enable Banking provider HTTP 401 usually
means the private key does not match the certificate registered for the
application. A callback 404 usually means the running API image does not include
the callback route; rebuild/use an image containing this backend branch.
A 500 on the bank page with `INTERNAL_ERROR` means an upstream provider call
failed; check the `bank request failed` log line for the cause.

## Troubleshooting

- Repeated `read Enable Banking private key` log lines indicate a permission or
  missing-file problem. Verify that the key is readable by the container user:

  ```sh
  docker run --rm --user 10001:10001 -v "$PWD/secrets:/run/secrets:ro" alpine:3.24.3 head -1 /run/secrets/enablebanking.key
  ```

## Cleanup

Disconnect the authorized connection using the authenticated
`DELETE /api/v1/households/{householdID}/bank/connections/{connectionID}` API
route, then stop the stack and remove the local environment file:

```sh
docker compose --env-file .env.local down
rm .env.local
```

The named database volume is retained by `down`; remove it only if you also want
to delete the local sandbox database. Keep the exported provider key in
`secrets/` only as long as it is needed for further testing.
