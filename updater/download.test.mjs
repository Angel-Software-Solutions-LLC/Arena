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
