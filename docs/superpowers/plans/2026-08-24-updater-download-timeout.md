# Updater Download Timeout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bound Arena updater tarball requests and streams so an HTTP stall becomes a recoverable update failure.

**Architecture:** Extract the download pipeline into an import-safe built-in-only module. One AbortController deadline spans fetch, error-body consumption, response streaming, and destination writing; the server retains GitHub URL/header construction and its existing update-state cleanup.

**Tech Stack:** Node.js 20+ ESM, built-in fetch/Web Streams, `node:test`, Docker.

**Spec:** `docs/superpowers/specs/2026-08-24-updater-download-timeout-design.md`

## Global Constraints

- The production download deadline is exactly `120_000` milliseconds.
- One AbortController signal covers the fetch and the entire file pipeline.
- Clear the deadline timer on success and on every failure path.
- Deadline failures expose a stable message naming the elapsed timeout; other errors preserve their existing behavior.
- Non-2xx response text remains truncated to 200 characters and never includes the request Authorization header.
- Preserve existing update-state, phase, temporary-path cleanup, URL, redirect, and optional-token behavior.
- Use Node built-ins only; do not change package or lockfiles.
- Update the updater Docker image to include every new runtime module.
- Do not touch Accounts/OIDC, customer data, game/config, SDK, CI workflow, frontend, or account documentation.
- Follow strict TDD and record the focused pre-implementation failures.

---

### Task 1: Bound the tarball download lifecycle

**Files:**
- Create: `updater/download.mjs`
- Create: `updater/download.test.mjs`
- Modify: `updater/server.mjs:20-27,82-90,722-750`
- Modify: `updater/Dockerfile:19-22`

**Interfaces:**
- Produces: `downloadTarballToFile({ url, headers, destinationPath, timeoutMs, fetchImpl }) -> Promise<void>`.
- Consumes: server-owned trusted GitHub URL/headers and a destination under the existing update temporary directory.

- [ ] **Step 1: Write success and timeout regressions**

Create `updater/download.test.mjs` exactly as follows:

```javascript
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { downloadTarballToFile } from "./download.mjs";

async function withTempDir(run) {
  const dir = await mkdtemp(join(tmpdir(), "arena-updater-download-test-"));
  try {
    await run(dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test("download writes the response body unchanged", async () => {
  await withTempDir(async (dir) => {
    const destinationPath = join(dir, "archive.tar.gz");

    await downloadTarballToFile({
      url: "https://example.invalid/archive",
      headers: {},
      destinationPath,
      timeoutMs: 1_000,
      fetchImpl: async () => new Response(Buffer.from("archive-bytes"))
    });

    assert.equal(await readFile(destinationPath, "utf8"), "archive-bytes");
  });
});

test("download bounds HTTP error text without echoing request headers", async () => {
  await withTempDir(async (dir) => {
    const destinationPath = join(dir, "archive.tar.gz");
    const token = "sensitive-token-sentinel";
    const responseText = "x".repeat(250);

    await assert.rejects(
      downloadTarballToFile({
        url: "https://example.invalid/archive",
        headers: { authorization: `token ${token}` },
        destinationPath,
        timeoutMs: 1_000,
        fetchImpl: async () => new Response(responseText, { status: 502 })
      }),
      (error) => {
        assert.equal(
          error.message,
          `GitHub tarball download failed: HTTP 502 ${"x".repeat(200)}`
        );
        assert.equal(error.message.includes(token), false);
        return true;
      }
    );
  });
});

test("download aborts a request that stalls before headers", async () => {
  await withTempDir(async (dir) => {
    const destinationPath = join(dir, "archive.tar.gz");
    let observedSignal;
    const stalledFetch = async (_url, { signal }) => {
      observedSignal = signal;
      return new Promise((_resolve, reject) => {
        signal.addEventListener("abort", () => reject(signal.reason), { once: true });
      });
    };

    await assert.rejects(
      downloadTarballToFile({
        url: "https://example.invalid/archive",
        headers: {},
        destinationPath,
        timeoutMs: 20,
        fetchImpl: stalledFetch
      }),
      /GitHub tarball download timed out after 20ms/
    );
    assert.equal(observedSignal.aborted, true);
  });
});

test("download aborts a response body that stalls", async () => {
  await withTempDir(async (dir) => {
    const destinationPath = join(dir, "archive.tar.gz");
    let observedSignal;
    const stalledFetch = async (_url, { signal }) => {
      observedSignal = signal;
      const body = new ReadableStream({
        start(controller) {
          signal.addEventListener(
            "abort",
            () => controller.error(signal.reason),
            { once: true }
          );
        }
      });
      return new Response(body, { status: 200 });
    };

    await assert.rejects(
      downloadTarballToFile({
        url: "https://example.invalid/archive",
        headers: {},
        destinationPath,
        timeoutMs: 20,
        fetchImpl: stalledFetch
      }),
      /GitHub tarball download timed out after 20ms/
    );
    assert.equal(observedSignal.aborted, true);
  });
});
```

All fetches are controlled test boundaries; no test calls the network.

- [ ] **Step 2: Verify the missing module is the only RED cause**

Run:

```bash
node --test updater/download.test.mjs
```

Expected: FAIL with `ERR_MODULE_NOT_FOUND` for `updater/download.mjs`.

- [ ] **Step 3: Implement the bounded built-in download helper**

Create `updater/download.mjs` exactly as follows:

```javascript
import { createWriteStream } from "node:fs";
import { mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";

export async function downloadTarballToFile({
  url,
  headers,
  destinationPath,
  timeoutMs,
  fetchImpl = globalThis.fetch
}) {
  const controller = new AbortController();
  const timeoutMessage = `GitHub tarball download timed out after ${timeoutMs}ms`;
  const timer = setTimeout(() => {
    controller.abort(new Error(timeoutMessage));
  }, timeoutMs);

  try {
    const response = await fetchImpl(url, {
      headers,
      redirect: "follow",
      signal: controller.signal
    });
    if (!response.ok || response.body === null) {
      const text = await response.text().catch(() => "");
      throw new Error(
        `GitHub tarball download failed: HTTP ${response.status} ${text.slice(0, 200)}`
      );
    }

    await mkdir(dirname(destinationPath), { recursive: true });
    await pipeline(
      Readable.fromWeb(response.body),
      createWriteStream(destinationPath),
      { signal: controller.signal }
    );
  } catch (error) {
    if (controller.signal.aborted) {
      throw new Error(timeoutMessage, { cause: error });
    }
    throw error;
  } finally {
    clearTimeout(timer);
  }
}
```

- [ ] **Step 4: Verify the downloader behavior**

Run:

```bash
node --test updater/download.test.mjs
node --check updater/download.mjs
```

Expected: 4/4 tests pass and syntax check exits 0 with no warnings.

- [ ] **Step 5: Wire the server and image**

In `updater/server.mjs`:

- remove imports used only by the old local pipeline;
- import `downloadTarballToFile` from `./download.mjs`;
- add `const GITHUB_TARBALL_TIMEOUT_MS = 120_000;`;
- keep the existing `downloadTarball(commitSha, githubToken, destinationPath)`
  wrapper responsible for URL and headers;
- replace its local fetch/status/pipeline block with:

```javascript
await downloadTarballToFile({
  url,
  headers,
  destinationPath,
  timeoutMs: GITHUB_TARBALL_TIMEOUT_MS
});
```

In `updater/Dockerfile`, include `download.mjs` in the existing runtime
`COPY` instruction.

- [ ] **Step 6: Verify updater and repository behavior**

Run from the repository root:

```bash
node --test updater/*.test.mjs
node --check updater/server.mjs
node --check updater/download.mjs
git diff --check
```

Expected: 24 updater tests pass; both syntax checks and the diff check exit 0.

- [ ] **Step 7: Commit the bounded downloader**

```bash
git add updater/download.mjs updater/download.test.mjs updater/server.mjs updater/Dockerfile
git commit -m "fix: bound updater source downloads"
```
