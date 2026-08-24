# SDK Resilience Design

## Goal

Keep the Python and Node.js bot SDKs aligned with the live WebSocket protocol
and prevent short-lived accepted connections from creating a one-second
reconnect loop.

## Current failures

The server emits `fog_radius` at the top level of every bot tick. The Python
SDK currently reads the value from `your_state`, so bot authors receive `0`
instead of the configured visibility radius. The Node.js SDK reads the correct
location already.

The Node.js reconnect loop resets its exponential delay immediately after the
WebSocket handshake succeeds. A server that accepts a connection and then
drops it quickly therefore produces repeated one-second reconnects. The Python
SDK already treats a connection as stable only after 30 seconds.

## Contract

- Python passes the top-level tick `fog_radius` to `on_tick` as
  `safe_zone.fog_radius`; an absent field falls back to `0`.
- Node.js starts reconnect delays at 1 second, doubles them after failed
  handshakes or ready sessions shorter than 30 seconds, and caps them at 30
  seconds.
- Node.js resets accumulated backoff to 1 second only after a ready connection
  lasted at least 30 seconds.
- Maintenance `retry_after_seconds` remains a lower bound on the next delay.
- No public SDK constructor, callback, action, or wire-message shape changes.
- No dependency or lockfile changes.

## Verification

Regression tests must demonstrate the old failures before implementation,
then pass with the fixes. Both SDK test suites, syntax/compile checks, the Go
repository gate, and `git diff --check` must pass before handoff.

## Coordination boundary

This work owns only the two SDK implementations and their existing connection
test files. It does not touch Accounts/OIDC, customer sessions, profiles,
Dashboard code, router construction, configuration, or account documentation
owned by the concurrent Arena session.
