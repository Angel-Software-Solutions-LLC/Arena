# Security

This document summarizes vulnerability reporting and the main security controls in AI Battle Arena.

## Reporting A Vulnerability

Use GitHub private vulnerability reporting or open a private security advisory. Do not open a public issue for undisclosed vulnerabilities.

Please include:

- affected commit or version
- affected endpoint, WebSocket message, SDK, or deployment surface
- reproduction steps
- expected impact
- any logs or proof of concept that do not expose third-party secrets

Maintainers will triage the report, coordinate a fix, and publish public details after the issue is addressed.

## Supported Versions

Security fixes target the current `main` branch unless a maintainer announces a release branch.

## Secrets

- Never commit API keys, admin tokens, database passwords, OIDC client secrets, Cloudflare tokens, or private keys.
- `.env` is ignored. `.env.example` contains placeholders only.
- Bot scripts should read credentials from environment variables such as `ARENA_API_KEY`.

## Authentication And Access Control

- Bot API keys are high-entropy server-issued values stored as rollback-safe composite credentials with short lookup prefixes. Each credential keeps a bcrypt prefix for older server versions and appends a versioned digest for fast current authentication. Legacy bcrypt rows remain valid and migrate to the composite after successful use; plaintext keys are shown once and cannot be recovered.
- Admin APIs admit a human only through an Angel Accounts product-administrator grant for Arena: a customer sign-in whose verified ID token carries `product_admin: true`. The grant is made per person, per product, from that person's record in the Accounts console. Working the support desk is not a way in — `staff: true` is recorded for audit and grants nothing, so a desk owner with no Arena grant has no more access than any customer. That authority is never persisted, lapses on `ARENA_OIDC_SESSION_TTL_HOURS`, and is re-read at the next sign-in, so revoking the grant revokes it. Arena's own admin SSO application and its `ARENA_OIDC_ADMIN_EMAILS` allowlist are retired.
- Machines authenticate separately and unchanged: `X-Admin-Token` carrying `ARENA_ADMIN_TOKEN` or a database-issued admin token. These are also the break-glass path if Angel Accounts is unreachable; there is no separate emergency human login.
- `ARENA_ADMIN_LOCALHOST_BYPASS` defaults to `true` for local development. Set it to `false` if a reverse proxy or container network could make untrusted traffic appear to originate from loopback.
- Set `ARENA_ADMIN_TOKEN` to a strong random value in every deployment.

## Network-Facing Hardening

- Redis-backed rate limiting covers key registration, WebSocket connection attempts, HTTP endpoints, and admin APIs.
- Bot names, stats, weapons, fallback behaviors, colors, and action payloads are validated server-side.
- WebSocket handlers enforce message size limits, heartbeat/dead-connection cleanup, and API key/IP bans.
- Security headers are enabled by default and must preserve same-origin framing for the dashboard/toolkit iframe flow.
- JSON request bodies are size-limited.
- The HTTP server uses read-header and idle timeouts.
- CORS origins are configurable with `ARENA_CORS_ORIGINS`.

## Data Protection

- Passwords and keys should not be logged.
- Admin key listings should mask key material except for safe hints.
- Database credentials and third-party tokens are loaded from environment variables.

## Self-Hosting Notes

- Keep Postgres and Redis bound to localhost or a private network.
- Terminate TLS at a reverse proxy.
- Rotate tokens after testing public demos or sharing logs.
- Review [docs/build-and-deploy.md](docs/build-and-deploy.md) before exposing a deployment to the internet.
