# Security and Operations — Hetzner VPS

**Deployment target:** one self-hosted Hetzner VPS for the initial production deployment. Keep operations reproducible and recoverable.

## Authentication and application security
- Email/password login with Argon2id (or a reviewed equivalent if a concrete compatibility blocker exists); parameters should be selected and benchmarked at implementation time.
- Sessions expire and can be individually revoked. Protect credentials on Android using platform-appropriate secure storage; do not store session tokens in ordinary unencrypted preferences.
- Only the owner manages household membership and household settings. Owners and members may manage transactions and invoke reasoned emergency overrides.
- Authorize every household-scoped read/write on the server. Never trust a client-supplied household ID without checking membership.
- Use TLS for external traffic. Do not expose PostgreSQL directly to the internet.
- Enforce request-size limits, timeouts, and rate limits on authentication/recovery paths. Use generic authentication errors to reduce account enumeration.
- Do not log passwords, session tokens, reset tokens, raw notification content, or sensitive financial payloads unnecessarily.

## Docker Compose deployment
- Docker Compose runs the Go API and PostgreSQL.
- PostgreSQL uses a persistent volume on a private Compose network; do not publish its port publicly.
- Expose only the reverse proxy's HTTPS port and carefully restricted SSH access.
- Caddy is the proposed low-maintenance reverse proxy for automatic TLS, pending implementation confirmation.
- Pin container image versions; avoid floating `latest` tags in production.
- Keep secrets out of source control and image layers. Restrict permissions on deployment secret files and rotate credentials if exposure is suspected.
- Apply host/container security updates. Run the application as non-root where practical. Configure restart policy, graceful shutdown, health checks and log rotation.

## Backups and recovery
- Back up PostgreSQL using a documented database-aware method.
- Encrypt backup artifacts before off-host transfer; use a separate failure domain, not only storage on the VPS. A Hetzner Storage Box or independent object-storage provider may be proposed, but select the target explicitly.
- Define retention, encryption-key custody, backup cadence, recovery point objective (RPO) and recovery time objective (RTO).
- Test restoring to a clean database/host before production and periodically afterward. Document required commands/secrets, migration order, application cutover and verification steps.
- Alert on backup failure, stale backup age, low disk space and service health failures. A successful backup command is not sufficient; restoration must be verified.

## Operational telemetry
- Provide liveness and readiness endpoints without exposing internal diagnostics publicly.
- Use structured logs with request/correlation IDs; redact sensitive fields. Avoid logging request bodies for financial/authentication endpoints by default.
- The domain journal is the financial audit trail; operational logs do not replace it.
- Monitor disk capacity, database availability, app restart loops and backup freshness.

## Open deployment choices
- DNS/domain and reverse-proxy confirmation (Caddy is proposed).
- Off-host backup target and retention.
- RPO/RTO and restore drill cadence.
- Monitoring/alerting provider.
- Whether a staging environment is needed.

Agents should propose concrete low-maintenance defaults suitable for a personal VPS and identify costs before introducing paid dependencies.
