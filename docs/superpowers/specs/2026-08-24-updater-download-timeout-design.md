# Updater Download Timeout Design

## Goal

Guarantee that a stalled GitHub source download cannot leave Arena's
Docker-socket updater sidecar permanently stuck in an in-progress state.

## Current failure

The updater applies explicit deadlines to archive extraction, rsync, Docker
inspection/build/stop/start, database migration, and internal status probes.
Its GitHub tarball fetch and streamed file write have no deadline. If either
the HTTP request or response body stalls, `runUpdate()` never exits, its
`finally` cleanup never runs, and `updateState.inProgress` remains true until
the sidecar is restarted.

## Design

Move the side-effect-free fetch-to-file operation into
`updater/download.mjs`. The helper owns an `AbortController` and one bounded
timer spanning the request, error-body read, response-body pipeline, and file
write. It passes the signal to both `fetch` and `pipeline`, clears the timer on
every exit, and translates deadline aborts into a stable operator-facing
timeout error while preserving other HTTP/network errors.

`updater/server.mjs` continues to own the trusted GitHub URL and optional
authorization header. It calls the helper with a fixed two-minute deadline.
The updater image must copy the new runtime module.

## Contract

- A request that does not produce headers before the deadline is aborted.
- A response body or file pipeline that does not finish before the deadline is
  aborted.
- Successful downloads preserve their bytes and do not leave an active timer.
- Non-2xx responses retain the existing bounded, token-safe error format.
- The production timeout is 120,000 milliseconds.
- No new package or lockfile dependency is introduced.
- Update-state failure handling and temporary-path cleanup remain owned by the
  existing `runUpdate()` and `startUpdate()` boundaries.

## Verification

Tests use real Web `Response`/`ReadableStream` bodies and a controlled fetch
boundary to prove success, bounded HTTP errors, stalled-request abort, and
stalled-body abort. The complete updater suite, syntax checks, Go gate, and
`git diff --check` must pass.

## Coordination boundary

This work changes only updater runtime/test files, its Dockerfile, and this
task's design/plan documents. It does not touch Accounts/OIDC, customer data,
game/config, SDK, CI workflow, frontend, or account documentation.
