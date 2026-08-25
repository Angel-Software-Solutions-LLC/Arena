/**
 * Internal-network-only sidecar that performs the actual "update the
 * app" work: fetch an exact commit's source tree from GitHub,
 * sync it into the deploy directory (never touching the data mounts), then
 * rebuild and recreate the app container (arena-server) via the host's Docker
 * daemon (mounted in at /var/run/docker.sock).
 *
 * This process holds real, root-equivalent power over the host: anything
 * that can make it act can run arbitrary commands as the host's Docker
 * daemon. It is deliberately kept out of the internet/tailnet-facing
 * app process and is published on no host port -- only reachable
 * from other containers on the compose project's internal network, by
 * service name. A shared-secret bearer token is required on top of that
 * network boundary as defense in depth. No dependencies: Node built-ins
 * only, to keep this process's own code surface as small as the privilege
 * it holds.
 */
import { createServer } from "node:http";
import { timingSafeEqual, randomUUID, createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdtemp, rm, readdir, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import {
  listReleaseFiles,
  readManifest,
  writeManifest,
  removeVanishedFiles
} from "./manifest.mjs";
import {
  buildComposeBaseArgs,
  buildMigrationInvocation,
  forceRemoveMigrationContainer,
  parseInspectedComposeContext,
  reconcileMigrationContainerAfterRunError,
  resolveDeployOwner
} from "./compose.mjs";
import {
  demobotsLatestCommit,
  demobotsState,
  startDemobotsUpdate
} from "./demobots.mjs";
import { downloadTarballToFile } from "./download.mjs";

const execFileAsync = promisify(execFile);

const PORT = Number(process.env.PORT ?? 8090);
const SHARED_SECRET = process.env.UPDATER_SHARED_SECRET ?? "";
const DEPLOY_DIR = process.env.DEPLOY_DIR ?? "/workspace";
const COMPOSE_SERVICE = process.env.COMPOSE_SERVICE ?? "arena-server";
// Fallbacks only. The project name and compose files are normally DETECTED from
// the live container's own compose labels at update time (see
// resolveComposeBaseArgs) so the sidecar always acts on the exact project and
// files the running container was created with, regardless of the sidecar's own
// working directory. These env values are used only if that detection fails
// (e.g. a first-ever deploy where nothing is running yet to read labels from).
const COMPOSE_FILES_FALLBACK = (process.env.COMPOSE_FILES ?? "docker-compose.yml")
  .split(/\s+/)
  .filter(Boolean);
const COMPOSE_PROJECT_FALLBACK = process.env.COMPOSE_PROJECT_NAME ?? null;
const MIGRATOR_DB_USER = process.env.ARENA_DB_MIGRATOR_USER ?? "";
const MIGRATOR_DB_PASSWORD = process.env.ARENA_DB_MIGRATOR_PASSWORD ?? "";
const RUNTIME_DB_USER = process.env.ARENA_RUNTIME_DB_USER ?? "";
const GITHUB_OWNER = process.env.GITHUB_OWNER ?? "ablac";
const GITHUB_REPO = process.env.GITHUB_REPO ?? "Arena";
const ARENA_INTERNAL_STATUS_URL = process.env.ARENA_INTERNAL_STATUS_URL ?? "http://arena-server:8000/internal/updater/status";
const MAX_BODY_BYTES = 16 * 1024;
const FULL_SHA_PATTERN = /^[0-9a-f]{40}$/;
const TMP_PREFIX = "arena-update-";
// Anchored with a leading "/" so rsync matches these only at the top of the
// transfer (DEPLOY_DIR itself), not at any depth. Arena keeps all runtime data
// in NAMED Docker volumes (arena_pgdata, arena_redis_data), not in bind mounts
// under the deploy dir, so there are no data directories to protect here -- only
// the two things that must never be overwritten by a fetched source tree: the
// deploy dir's own git metadata and its .env secrets. The sync never uses
// --delete, so anything not in the fetched tree (local-only bots, etc.) is left
// in place.
const EXCLUDED_PATHS = ["/.git", "/.env"];
// Arena has no bind-mounted data directories in the deploy dir (runtime data is
// in named volumes), so the ownership-restore pass may chown every top-level
// entry back to the operator with nothing to exempt.
const OWNERSHIP_EXEMPT_NAMES = new Set();
const GITHUB_TARBALL_TIMEOUT_MS = 120_000;
const TAR_TIMEOUT_MS = 2 * 60 * 1000;
const RSYNC_TIMEOUT_MS = 2 * 60 * 1000;
const DOCKER_INSPECT_TIMEOUT_MS = 30 * 1000;
const DOCKER_BUILD_TIMEOUT_MS = 20 * 60 * 1000;
const DOCKER_STOP_TIMEOUT_MS = 30 * 1000;
const DB_MIGRATION_TIMEOUT_MS = 10 * 60 * 1000;
const DOCKER_UP_TIMEOUT_MS = 5 * 60 * 1000;

// The app container -- the very thing an update recreates -- is
// what's holding the browser's original HTTP connection open, so a request
// that awaits the whole fetch+build+recreate pipeline before responding can
// never deliver its response: the connection is severed out from under it
// partway through. POST /update instead validates, records state, and
// returns 202 immediately once the work is queued; runUpdate() proceeds in
// the background (this process is never recreated, only the app container
// is) and GET /status reports progress for the browser to poll, proxied
// through the app's own admin update-status route.
let updateState = {
  inProgress: false,
  targetCommit: null,
  phase: null,
  startedAt: null,
  finishedAt: null,
  lastError: null,
  lastCompletedCommit: null
};

const server = createServer((request, response) => {
  handleRequest(request, response).catch((error) => {
    log("Unhandled request error", error);
    if (!response.headersSent) {
      response.writeHead(500, { "content-type": "application/json" });
    }
    response.end(JSON.stringify({ ok: false, error: "internal error" }));
  });
});

async function handleRequest(request, response) {
  if (request.method === "GET" && request.url === "/healthz") {
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: true }));
    return;
  }

  if (request.method === "GET" && request.url === "/status") {
    if (SHARED_SECRET === "" || !isAuthorized(request)) {
      response.writeHead(401, { "content-type": "application/json" });
      response.end(JSON.stringify({ ok: false, error: "unauthorized" }));
      return;
    }
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: true, ...updateState, demobots: demobotsState() }));
    return;
  }

  if (request.method === "GET" && request.url === "/demobots/latest") {
    if (SHARED_SECRET === "" || !isAuthorized(request)) {
      response.writeHead(401, { "content-type": "application/json" });
      response.end(JSON.stringify({ ok: false, error: "unauthorized" }));
      return;
    }
    try {
      const commit = await demobotsLatestCommit();
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ ok: true, commit }));
    } catch (error) {
      response.writeHead(502, { "content-type": "application/json" });
      response.end(JSON.stringify({ ok: false, error: error.message }));
    }
    return;
  }

  if (request.method === "POST" && request.url === "/demobots/update") {
    if (SHARED_SECRET === "" || !isAuthorized(request)) {
      response.writeHead(401, { "content-type": "application/json" });
      response.end(JSON.stringify({ ok: false, error: "unauthorized" }));
      return;
    }
    let demobotsBody;
    try {
      demobotsBody = await readJsonBody(request);
    } catch (error) {
      response.writeHead(400, { "content-type": "application/json" });
      response.end(JSON.stringify({ ok: false, error: error.message }));
      return;
    }
    try {
      const result = await startDemobotsUpdate(
        typeof demobotsBody.commitSha === "string" ? demobotsBody.commitSha : "",
        undefined,
        log
      );
      response.writeHead(202, { "content-type": "application/json" });
      response.end(JSON.stringify({ ok: true, ...result }));
    } catch (error) {
      response.writeHead(error.statusCode ?? 502, { "content-type": "application/json" });
      response.end(JSON.stringify({ ok: false, error: error.message }));
    }
    return;
  }

  if (request.method !== "POST" || request.url !== "/update") {
    response.writeHead(404, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: false, error: "not found" }));
    return;
  }

  if (SHARED_SECRET === "" || !isAuthorized(request)) {
    response.writeHead(401, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: false, error: "unauthorized" }));
    return;
  }

  let body;
  try {
    body = await readJsonBody(request);
  } catch (error) {
    response.writeHead(400, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: false, error: error.message }));
    return;
  }

  const commitSha = typeof body.commitSha === "string" ? body.commitSha : "";
  const githubToken = typeof body.githubToken === "string" ? body.githubToken : "";
  if (!FULL_SHA_PATTERN.test(commitSha)) {
    response.writeHead(400, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: false, error: "commitSha must be a full 40-character lowercase commit SHA" }));
    return;
  }
  // Arena is a PUBLIC repo, so a token is OPTIONAL -- the tarball fetch works
  // unauthenticated. Accept one if provided (raises the GitHub rate limit), but
  // do not require it. Still bound its length as a sanity check.
  if (githubToken.length > 4096) {
    response.writeHead(400, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: false, error: "githubToken too long" }));
    return;
  }

  if (updateState.inProgress) {
    response.writeHead(409, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: false, error: "an update is already in progress" }));
    return;
  }

  updateState = {
    inProgress: true,
    targetCommit: commitSha,
    phase: "fetching",
    startedAt: new Date().toISOString(),
    finishedAt: null,
    lastError: null,
    lastCompletedCommit: updateState.lastCompletedCommit
  };
  response.writeHead(202, { "content-type": "application/json" });
  response.end(JSON.stringify({ ok: true, accepted: true, commitSha }));

  runUpdate(commitSha, githubToken, (phase) => {
    updateState = { ...updateState, phase };
  })
    .then(() => {
      updateState = {
        ...updateState,
        inProgress: false,
        phase: "done",
        finishedAt: new Date().toISOString(),
        lastCompletedCommit: commitSha
      };
      log(`Update to ${commitSha} complete`);
    })
    .catch(async (error) => {
      log(`Update to ${commitSha} failed`, error);
	  await notifyArenaStatus("failed", commitSha).catch((notifyError) => {
	    log("Could not clear Arena maintenance status after update failure", notifyError);
	  });
      updateState = {
        ...updateState,
        inProgress: false,
        phase: "failed",
        finishedAt: new Date().toISOString(),
        lastError: error.message
      };
    });
}

/**
 * Hashes both sides to a fixed-length digest before comparing, so
 * timingSafeEqual always runs (a raw length check before it would leak the
 * secret's exact byte length to anyone who can reach this endpoint on the
 * internal network, since a length mismatch would return faster than a
 * length match).
 */
function isAuthorized(request) {
  const header = request.headers.authorization ?? "";
  const expected = `Bearer ${SHARED_SECRET}`;
  const providedHash = createHash("sha256").update(header).digest();
  const expectedHash = createHash("sha256").update(expected).digest();

  return timingSafeEqual(providedHash, expectedHash);
}

function readJsonBody(request) {
  return new Promise((resolve, reject) => {
    let size = 0;
    const chunks = [];
    request.on("data", (chunk) => {
      size += chunk.length;
      if (size > MAX_BODY_BYTES) {
        reject(new Error("request body too large"));
        request.destroy();
        return;
      }
      chunks.push(chunk);
    });
    request.on("end", () => {
      try {
        resolve(JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}"));
      } catch {
        reject(new Error("invalid JSON body"));
      }
    });
    request.on("error", reject);
  });
}

async function runUpdate(commitSha, githubToken, onPhase) {
  const workId = randomUUID();
  const migrationContainerName = `arena-migration-${workId}`;
  // Validate the release-only role identities before fetching or syncing any
  // source. buildMigrationInvocation is called again with the detected compose
  // context after the new image is built.
  buildMigrationInvocation({
    composeBaseArgs: [],
    service: COMPOSE_SERVICE,
    migratorUser: MIGRATOR_DB_USER,
    migratorPassword: MIGRATOR_DB_PASSWORD,
    runtimeUser: RUNTIME_DB_USER,
    containerName: migrationContainerName
  });

  const tarballPath = join(tmpdir(), `${TMP_PREFIX}${workId}.tar.gz`);
  const stagingDir = await mkdtemp(join(tmpdir(), `${TMP_PREFIX}${workId}-`));

  try {
    onPhase("fetching");
    log(`Fetching ${GITHUB_OWNER}/${GITHUB_REPO}@${commitSha}`);
    await downloadTarball(commitSha, githubToken, tarballPath);

    onPhase("extracting");
    log(`Extracting to ${stagingDir}`);
    await execFileAsync(
      "tar",
      ["-xzf", tarballPath, "-C", stagingDir, "--strip-components=1"],
      { timeout: TAR_TIMEOUT_MS }
    );

    // Git (and therefore a GitHub tarball) can carry symlinks as first-class
    // tree entries. DEPLOY_DIR is both the live deploy directory and the
    // `docker compose build` context, so an uninspected symlink landing there
    // (e.g. one pointing outside the tree) is a foothold for anything later
    // that reads a repo-relative path without checking for one. Fail the
    // whole update rather than silently accepting one -- this repo has no
    // legitimate symlinks today.
    onPhase("checking");
    await assertNoSymlinks(stagingDir);

    onPhase("syncing");
    log(`Syncing into ${DEPLOY_DIR}`);
    // Deliberately no --delete: the deploy directory legitimately holds
    // files that only ever exist on the VPS and were never committed to git
    // (docker-compose.override.yml, .env); the first live use of --delete
    // destroyed one of those and broke the next build. Repo-owned deletions
    // are applied separately below via the release manifest: only files the
    // PREVIOUS fetched release actually shipped, and the new release no
    // longer ships, are removed. Operator-only files are never in a
    // manifest, so they can never be deleted. Without this, a file deleted
    // upstream lingered forever — the demobots refactor left an old Go file
    // beside its replacements and the duplicate declarations broke the
    // production build (2026-07-14).
    const releaseFiles = await listReleaseFiles(stagingDir);
    const previousRelease = await readManifest(DEPLOY_DIR);
    await execFileAsync(
      "rsync",
      ["-a", ...EXCLUDED_PATHS.map((path) => `--exclude=${path}`), `${stagingDir}/`, `${DEPLOY_DIR}/`],
      { timeout: RSYNC_TIMEOUT_MS }
    );
    if (previousRelease) {
      const removed = await removeVanishedFiles(DEPLOY_DIR, previousRelease, releaseFiles, EXCLUDED_PATHS);
      if (removed.length > 0) {
        log(`Removed ${removed.length} file(s) deleted upstream: ${removed.join(", ")}`);
      }
    } else {
      log("No previous release manifest — skipping upstream-deletion sync for this update");
    }
    await writeManifest(DEPLOY_DIR, commitSha, releaseFiles);

    onPhase("fixing-ownership");
    await restoreOwnership();

    // Resolve the compose project + files ONCE from the live container's labels
    // (see resolveComposeBaseArgs) and reuse the identical base args for both
    // build and up, so the recreate always targets the same project the running
    // container already belongs to -- never a fresh project derived from this
    // sidecar's own working-directory basename.
    const composeBaseArgs = await resolveComposeBaseArgs();

    onPhase("building");
    log(`Building the ${COMPOSE_SERVICE} image (docker compose ${composeBaseArgs.join(" ")} build ${COMPOSE_SERVICE})`);
    // GIT_COMMIT + BUILD_TIME are the build args Arena's Dockerfile bakes into
    // the binary (ldflags) so GET /api/v1/version reports the live commit.
    await execFileAsync("docker", ["compose", ...composeBaseArgs, "build", COMPOSE_SERVICE], {
      cwd: DEPLOY_DIR,
      env: { ...process.env, GIT_COMMIT: commitSha, BUILD_TIME: new Date().toISOString() },
      timeout: DOCKER_BUILD_TIMEOUT_MS
    });

    onPhase("draining");
    await notifyArenaStatus("restarting", commitSha).catch((error) => {
      // The app already persisted the preparing notice before accepting the
      // update. This callback sharpens the phase but must not prevent an
      // otherwise valid security/release update.
      log("Could not notify Arena before recreation", error);
    });
    await delay(600);

    // Quiesce the old writer before owner-run migrations. Besides DDL, schema
    // setup can promote persisted balance-controller epochs and backfill round
    // identity; allowing the old binary to keep writing during those changes
    // would mix incompatible state. `stop` retains the old container and image,
    // which gives us a safe recovery path if migration fails.
    onPhase("stopping");
    log(`Stopping the old ${COMPOSE_SERVICE} container before database migration`);
    let migrationContainerRemoved = false;
    let recoverySafe = true;
    try {
      await execFileAsync("docker", ["compose", ...composeBaseArgs, "stop", "-t", "15", COMPOSE_SERVICE], {
        cwd: DEPLOY_DIR,
        timeout: DOCKER_STOP_TIMEOUT_MS
      });

      onPhase("migrating");
      const migration = buildMigrationInvocation({
        composeBaseArgs,
        service: COMPOSE_SERVICE,
        migratorUser: MIGRATOR_DB_USER,
        migratorPassword: MIGRATOR_DB_PASSWORD,
        runtimeUser: RUNTIME_DB_USER,
        containerName: migrationContainerName
      });
      log(`Migrating the database with the freshly built ${COMPOSE_SERVICE} image`);
      try {
        await execFileAsync("docker", migration.args, {
          cwd: DEPLOY_DIR,
          env: { ...process.env, ...migration.env },
          timeout: DB_MIGRATION_TIMEOUT_MS
        });
      } catch (runError) {
        let reconciliation;
        try {
          reconciliation = await reconcileMigrationContainerAfterRunError({
            execFile: execFileAsync,
            containerName: migrationContainerName,
            options: { cwd: DEPLOY_DIR, timeout: DOCKER_STOP_TIMEOUT_MS }
          });
          migrationContainerRemoved = true;
        } catch (cleanupError) {
          recoverySafe = false;
          throw new Error(
            `database migration command failed (${errorMessage(runError)}); ` +
              `its container state could not be resolved (${errorMessage(cleanupError)}), ` +
              `so the previous app was not restarted`
          );
        }
        if (!reconciliation.migrationSucceeded) {
          throw runError;
        }
        log(`Migration container completed successfully after its Compose client failed (${reconciliation.state})`);
      }

      if (!migrationContainerRemoved) {
        try {
          await forceRemoveMigrationContainer({
            execFile: execFileAsync,
            containerName: migrationContainerName,
            options: { cwd: DEPLOY_DIR, timeout: DOCKER_STOP_TIMEOUT_MS }
          });
          migrationContainerRemoved = true;
        } catch (cleanupError) {
          recoverySafe = false;
          throw new Error(
            `database migration completed but its container could not be removed (${errorMessage(cleanupError)}), ` +
              `so the previous app was not restarted`
          );
        }
      }
    } catch (migrationError) {
      onPhase("recovering");
      if (!recoverySafe) {
        throw migrationError;
      }
      log(`Migration failed; restarting the retained old ${COMPOSE_SERVICE} container`, migrationError);
      try {
        await execFileAsync("docker", ["compose", ...composeBaseArgs, "start", COMPOSE_SERVICE], {
          cwd: DEPLOY_DIR,
          timeout: DOCKER_UP_TIMEOUT_MS
        });
        await waitForArenaReachable();
      } catch (recoveryError) {
        throw new Error(
          `database migration failed (${errorMessage(migrationError)}); ` +
            `the previous app could not be restored (${errorMessage(recoveryError)})`
        );
      }
      throw new Error(`database migration failed; previous app restored: ${errorMessage(migrationError)}`);
    }

    onPhase("recreating");
    // --no-deps: recreate ONLY the app service, never its compose dependencies.
    // arena-server depends_on arena-db + arena-redis, and a plain `up -d
    // arena-server` also recreates those (verified: it recreates arena-db),
    // which needlessly interrupts the database on every update. The dependencies
    // are already running and healthy; the recreated app just reconnects to them
    // over the network.
    log(`Recreating the ${COMPOSE_SERVICE} container (docker compose ${composeBaseArgs.join(" ")} up -d --no-deps ${COMPOSE_SERVICE})`);
    await execFileAsync("docker", ["compose", ...composeBaseArgs, "up", "-d", "--no-deps", COMPOSE_SERVICE], {
      cwd: DEPLOY_DIR,
      timeout: DOCKER_UP_TIMEOUT_MS
    });

    onPhase("verifying");
    await waitForArenaCommit(commitSha);
    await notifyArenaStatus("done", commitSha).catch((error) => {
      // Startup reconciliation in arena-server also clears a matching target,
      // so callback failure here is observable but not correctness-critical.
      log("Could not notify Arena that the update completed", error);
    });
  } finally {
    await rm(tarballPath, { force: true }).catch(() => undefined);
    await rm(stagingDir, { recursive: true, force: true }).catch(() => undefined);
  }
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function notifyArenaStatus(phase, targetCommit) {
  if (SHARED_SECRET === "") {
    throw new Error("UPDATER_SHARED_SECRET is not configured");
  }
  const response = await fetch(ARENA_INTERNAL_STATUS_URL, {
    method: "POST",
    headers: {
      authorization: `Bearer ${SHARED_SECRET}`,
      "content-type": "application/json"
    },
    body: JSON.stringify({ phase, target_commit: targetCommit }),
    signal: AbortSignal.timeout(3000)
  });
  if (!response.ok) {
    const text = await response.text().catch(() => "");
    throw new Error(`Arena status callback returned ${response.status}: ${text.slice(0, 160)}`);
  }
}

async function waitForArenaCommit(targetCommit) {
  const deadline = Date.now() + 60_000;
  const versionURL = new URL("/api/v1/version", ARENA_INTERNAL_STATUS_URL).toString();
  while (Date.now() < deadline) {
    try {
      const response = await fetch(versionURL, { signal: AbortSignal.timeout(2500) });
      if (response.ok) {
        const body = await response.json();
        if (body.commit === targetCommit) return;
      }
    } catch {
      // Expected while compose is replacing the app container.
    }
    await delay(1000);
  }
  throw new Error(`Arena did not report target commit ${targetCommit} within 60 seconds`);
}

async function waitForArenaReachable() {
  const deadline = Date.now() + 60_000;
  const versionURL = new URL("/api/v1/version", ARENA_INTERNAL_STATUS_URL).toString();
  while (Date.now() < deadline) {
    try {
      const response = await fetch(versionURL, { signal: AbortSignal.timeout(2500) });
      if (response.ok) return;
    } catch {
      // Expected while the retained app container starts again.
    }
    await delay(1000);
  }
  throw new Error("previous Arena container did not become reachable within 60 seconds");
}

function errorMessage(error) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Decide the `docker compose` project + file flags for this update by reading
 * them off the LIVE app container's own compose labels, so build and
 * recreate always act on the exact project (and exact compose files) that
 * container was created with. Falls back to the COMPOSE_PROJECT_NAME /
 * COMPOSE_FILES env vars only when the container can't be inspected (nothing
 * running yet). Without this, compose derives the project from THIS sidecar's
 * working-directory basename (e.g. "workspace"), which does not match the live
 * project, so it tries to create a second container with an already-in-use
 * container_name and fails -- the original "Conflict" bug.
 */
async function resolveComposeBaseArgs() {
  const detected = await detectComposeContext(COMPOSE_SERVICE);
  const baseArgs = buildComposeBaseArgs({
    detectedProject: detected?.project ?? null,
    detectedFiles: detected?.files ?? null,
    envProject: COMPOSE_PROJECT_FALLBACK,
    envFiles: COMPOSE_FILES_FALLBACK
  });
  if (baseArgs.length === 0) {
    log("Warning: no compose project or files resolved; compose will fall back to its cwd basename");
  }
  return baseArgs;
}

async function detectComposeContext(service) {
  try {
    const { stdout } = await execFileAsync(
      "docker",
      [
        "inspect",
        service,
        "--format",
        '{{index .Config.Labels "com.docker.compose.project"}}\t{{index .Config.Labels "com.docker.compose.project.working_dir"}}\t{{index .Config.Labels "com.docker.compose.project.config_files"}}'
      ],
      { timeout: DOCKER_INSPECT_TIMEOUT_MS }
    );
    const context = parseInspectedComposeContext(stdout);
    if (context.project !== null) {
      log(`Detected compose project "${context.project}" (files: ${context.files.join(", ") || "none"}) from the running ${service} container`);
    }
    return context;
  } catch (error) {
    log(`Could not detect compose context from ${service}; using env fallback`, error);
    return null;
  }
}

/**
 * This container runs as root (it needs to be, to reach the Docker socket),
 * and `rsync -a` preserves the SOURCE's ownership -- the just-extracted
 * GitHub tarball, owned by root since extraction also ran as root. Without
 * this step, every synced code file would end up root-owned on the host,
 * silently breaking the documented convention that the deploy directory's
 * code is owned by the operator (keith), which the manual git-archive
 * deploy path depends on to even write into the directory. Restores
 * ownership to whatever already owns DEPLOY_DIR itself (never touched by
 * the sync, so it is a reliable, host-portable reference point -- no
 * hardcoded UID), on every top-level entry except the data-mount
 * directories, which must stay owned by the container UID the app itself
 * runs as.
 *
 * Guard: if DEPLOY_DIR is itself root-owned (uid 0) -- the corrupted state a
 * prior failed update can leave -- restoring TO root would cement it and lock
 * the non-root operator out of their own deploy dir. In that case, honor an
 * explicit DEPLOY_OWNER ("uid:gid") if set, otherwise skip and warn rather than
 * perpetuate root ownership. See resolveDeployOwner in compose.mjs.
 */
async function restoreOwnership() {
  let owner;
  try {
    const info = await stat(DEPLOY_DIR);
    owner = resolveDeployOwner({
      envOwner: process.env.DEPLOY_OWNER,
      statUid: info.uid,
      statGid: info.gid
    });
  } catch (error) {
    log("Ownership restore: failed to stat DEPLOY_DIR, skipping", error);
    return;
  }

  if (owner === null) {
    log(
      "Ownership restore: DEPLOY_DIR is root-owned and no valid DEPLOY_OWNER is set; " +
        "skipping to avoid locking the operator out (set DEPLOY_OWNER=uid:gid to restore explicitly)"
    );
    return;
  }

  let entries;
  try {
    entries = await readdir(DEPLOY_DIR, { withFileTypes: true });
  } catch (error) {
    log("Ownership restore: failed to list DEPLOY_DIR, skipping", error);
    return;
  }

  const targets = entries
    .filter((entry) => !OWNERSHIP_EXEMPT_NAMES.has(entry.name))
    .map((entry) => join(DEPLOY_DIR, entry.name));

  try {
    // Restore DEPLOY_DIR itself first (non-recursively -- the -R below handles
    // code; the data mounts it contains are left alone). `rsync -a` copies the
    // root-owned staging dir's owner AND perms onto DEPLOY_DIR on every sync,
    // re-rooting it (and dropping it to 700) each time, which locks the non-root
    // operator out of their own deploy directory. Chown it back and ensure it
    // stays traversable.
    await execFileAsync("chown", [owner, DEPLOY_DIR], { timeout: RSYNC_TIMEOUT_MS });
    await execFileAsync("chmod", ["755", DEPLOY_DIR], { timeout: RSYNC_TIMEOUT_MS });
    if (targets.length > 0) {
      await execFileAsync("chown", ["-R", owner, ...targets], { timeout: RSYNC_TIMEOUT_MS });
    }
  } catch (error) {
    // Best-effort: this only affects a later manual (non-sidecar) deploy,
    // not the running container or the rest of this update, so log and
    // move on rather than failing the whole update over it.
    log("Ownership restore failed (non-fatal)", error);
  }
}

async function assertNoSymlinks(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  for (const entry of entries) {
    const entryPath = join(dir, entry.name);
    if (entry.isSymbolicLink()) {
      throw new Error(`Refusing to sync: fetched tree contains a symlink at ${entryPath}`);
    }
    if (entry.isDirectory()) {
      await assertNoSymlinks(entryPath);
    }
  }
}

async function downloadTarball(commitSha, githubToken, destinationPath) {
  const url = `https://api.github.com/repos/${GITHUB_OWNER}/${GITHUB_REPO}/tarball/${commitSha}`;
  // GitHub's tarball endpoint always 302s to codeload.github.com as normal,
  // required behavior -- redirect: "manual" would break every download. The
  // target host is fixed above, not attacker-steerable per-request. Arena is a
  // PUBLIC repo, so no auth is needed; a token is sent only if one was provided
  // (to raise the anonymous 60-req/hr rate limit) and is scoped to this one
  // read.
  const headers = {
    accept: "application/vnd.github+json",
    "user-agent": "arena-updater"
  };
  if (githubToken !== "") {
    headers.authorization = `token ${githubToken}`;
  }
  await downloadTarballToFile({
    url,
    headers,
    destinationPath,
    timeoutMs: GITHUB_TARBALL_TIMEOUT_MS
  });
}

/**
 * A hard kill (OOM, forced container removal, host power loss) mid-update
 * skips the finally block in runUpdate(), leaking the tarball + staging
 * directory under /tmp. Nothing else ever cleans these up, so sweep them
 * once at startup.
 */
async function sweepStaleTempPaths() {
  let entries;
  try {
    entries = await readdir(tmpdir());
  } catch (error) {
    log("Startup temp sweep: failed to list tmpdir", error);
    return;
  }

  const stale = entries.filter((name) => name.startsWith(TMP_PREFIX));
  for (const name of stale) {
    await rm(join(tmpdir(), name), { recursive: true, force: true }).catch((error) => {
      log(`Startup temp sweep: failed to remove ${name}`, error);
    });
  }
  if (stale.length > 0) {
    log(`Startup temp sweep: removed ${stale.length} stale path(s) from a prior run`);
  }
}

function log(message, error) {
  const timestamp = new Date().toISOString();
  if (error === undefined) {
    console.log(`[${timestamp}] ${message}`);
  } else {
    console.error(`[${timestamp}] ${message}: ${error instanceof Error ? error.message : String(error)}`);
  }
}

await sweepStaleTempPaths();
server.listen(PORT, "0.0.0.0", () => {
  log(`Updater listening on :${PORT} (internal network only; no host port is published)`);
});
