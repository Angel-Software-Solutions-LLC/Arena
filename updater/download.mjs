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
