/**
 * Demo-bot fleet updates. The fleet's source lives in a PRIVATE repo
 * (github.com/ablac/arena-demobots), so unlike the main app's public-tarball
 * flow, the sidecar fetches it over SSH with a read-only deploy key that
 * lives in the fleet's deploy directory on the host. The update is
 * image-based: shallow-fetch the requested commit, `docker build` the fleet
 * image from the checkout (the build context streams from this container's
 * filesystem, so the clone never has to be host-visible), then recreate the
 * fleet container via its own compose project. No manifest/rsync machinery
 * is needed because nothing is overlaid onto a deploy tree.
 *
 * Everything here no-ops with a clear error when DEMOBOTS_GIT_URL is unset,
 * so public-repo deployments without a private fleet lose nothing.
 */
import { execFile } from "node:child_process";
import { mkdtemp, rm, access } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, posix } from "node:path";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const GIT_TIMEOUT_MS = 2 * 60 * 1000;
const BUILD_TIMEOUT_MS = 15 * 60 * 1000;
const COMPOSE_TIMEOUT_MS = 5 * 60 * 1000;
const FULL_SHA_PATTERN = /^[0-9a-f]{40}$/;
const TMP_PREFIX = "demobots-update-";

export function demobotsConfigFromEnv(env = process.env) {
  return {
    gitUrl: env.DEMOBOTS_GIT_URL ?? "",
    branch: env.DEMOBOTS_BRANCH ?? "main",
    deployDir: env.DEMOBOTS_DEPLOY_DIR ?? "/demobots-deploy",
    image: env.DEMOBOTS_IMAGE ?? "arena-demobots:local",
    composeService: env.DEMOBOTS_COMPOSE_SERVICE ?? "arena-demobots",
    composeProject: env.DEMOBOTS_COMPOSE_PROJECT ?? "arena-demobots"
  };
}

/** The deploy key is operator-provisioned at a fixed name inside the fleet's
 * deploy dir, which is already the one host path this feature mounts. */
export function deployKeyPath(config) {
  return posix.join(config.deployDir, ".deploy-key");
}

/**
 * Build the GIT_SSH_COMMAND for the deploy key. accept-new pins github.com's
 * host key on first use inside the container (an ephemeral known_hosts is
 * fine: the deploy key is read-only and scoped to this single repo, so a
 * MITM could at most serve different source, which lands in a private image,
 * not the public app).
 */
export function buildGitSSHCommand(keyPath) {
  return `ssh -i ${keyPath} -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/tmp/demobots-known-hosts`;
}

/** Parse `git ls-remote <url> <ref>` output into a full commit SHA. */
export function parseLsRemote(stdout) {
  const first = String(stdout).split("\n", 1)[0] ?? "";
  const sha = first.split("\t", 1)[0]?.trim() ?? "";
  return FULL_SHA_PATTERN.test(sha) ? sha : null;
}

export function isFullSha(value) {
  return typeof value === "string" && FULL_SHA_PATTERN.test(value);
}

// Same shape as the main app's update state so the admin panel can poll both
// through one status payload.
let state = {
  configured: false,
  inProgress: false,
  targetCommit: null,
  phase: null,
  startedAt: null,
  finishedAt: null,
  lastError: null,
  lastCompletedCommit: null
};

export function demobotsState(config = demobotsConfigFromEnv()) {
  return { ...state, configured: config.gitUrl !== "" };
}

async function gitEnv(config) {
  const keyPath = deployKeyPath(config);
  await access(keyPath).catch(() => {
    throw new Error(`demobots deploy key not found at ${keyPath}`);
  });
  return { ...process.env, GIT_SSH_COMMAND: buildGitSSHCommand(keyPath) };
}

/** Resolve the private repo's branch tip over SSH (no clone). */
export async function demobotsLatestCommit(config = demobotsConfigFromEnv()) {
  if (config.gitUrl === "") {
    throw new Error("DEMOBOTS_GIT_URL is not configured");
  }
  const env = await gitEnv(config);
  const { stdout } = await execFileAsync(
    "git",
    ["ls-remote", config.gitUrl, `refs/heads/${config.branch}`],
    { env, timeout: GIT_TIMEOUT_MS }
  );
  const sha = parseLsRemote(stdout);
  if (sha === null) {
    throw new Error(`could not resolve ${config.branch} tip from ls-remote output`);
  }
  return sha;
}

/**
 * Start a fleet update in the background. Returns {accepted, commitSha} or
 * throws for validation errors. Mirrors the main flow's 202-then-poll model.
 */
export async function startDemobotsUpdate(requestedSha, config = demobotsConfigFromEnv(), log = console.log) {
  if (config.gitUrl === "") {
    throw new Error("DEMOBOTS_GIT_URL is not configured");
  }
  if (state.inProgress) {
    const err = new Error("a demobots update is already in progress");
    err.statusCode = 409;
    throw err;
  }
  const commitSha = requestedSha === "" || requestedSha === undefined || requestedSha === null
    ? await demobotsLatestCommit(config)
    : requestedSha;
  if (!isFullSha(commitSha)) {
    const err = new Error("commitSha must be a full 40-character lowercase commit SHA (or omitted for the branch tip)");
    err.statusCode = 400;
    throw err;
  }

  state = {
    configured: true,
    inProgress: true,
    targetCommit: commitSha,
    phase: "fetching",
    startedAt: new Date().toISOString(),
    finishedAt: null,
    lastError: null,
    lastCompletedCommit: state.lastCompletedCommit
  };

  runDemobotsUpdate(commitSha, config, log)
    .then(() => {
      state = {
        ...state,
        inProgress: false,
        phase: "done",
        finishedAt: new Date().toISOString(),
        lastCompletedCommit: commitSha
      };
      log(`Demobots update to ${commitSha} complete`);
    })
    .catch((error) => {
      log(`Demobots update to ${commitSha} failed: ${error.message}`);
      state = {
        ...state,
        inProgress: false,
        phase: "failed",
        finishedAt: new Date().toISOString(),
        lastError: error.message
      };
    });

  return { accepted: true, commitSha };
}

async function runDemobotsUpdate(commitSha, config, log) {
  const setPhase = (phase) => {
    state = { ...state, phase };
  };
  const checkout = await mkdtemp(join(tmpdir(), TMP_PREFIX));
  try {
    setPhase("fetching");
    log(`Fetching arena-demobots@${commitSha}`);
    const env = await gitEnv(config);
    // init + fetch-by-sha instead of clone: it works for any reachable commit,
    // not just the branch tip, and never pulls more than one tree.
    await execFileAsync("git", ["init", "-q", checkout], { env, timeout: GIT_TIMEOUT_MS });
    await execFileAsync("git", ["-C", checkout, "fetch", "-q", "--depth", "1", config.gitUrl, commitSha], {
      env,
      timeout: GIT_TIMEOUT_MS
    });
    await execFileAsync("git", ["-C", checkout, "checkout", "-q", "FETCH_HEAD"], { env, timeout: GIT_TIMEOUT_MS });

    setPhase("building");
    log(`Building ${config.image} from ${commitSha}`);
    await execFileAsync(
      "docker",
      [
        "build",
        "-t", config.image,
        "--build-arg", `GIT_COMMIT=${commitSha}`,
        "--build-arg", `BUILD_TIME=${new Date().toISOString()}`,
        checkout
      ],
      { timeout: BUILD_TIMEOUT_MS }
    );

    setPhase("recreating");
    log(`Recreating the ${config.composeService} container`);
    await execFileAsync(
      "docker",
      [
        "compose",
        "-p", config.composeProject,
        "-f", posix.join(config.deployDir, "docker-compose.yml"),
        "up", "-d", "--force-recreate", config.composeService
      ],
      { timeout: COMPOSE_TIMEOUT_MS }
    );

    setPhase("verifying");
    const { stdout: builtId } = await execFileAsync(
      "docker",
      ["image", "inspect", config.image, "--format", "{{.Id}}"],
      { timeout: GIT_TIMEOUT_MS }
    );
    const { stdout: runningId } = await execFileAsync(
      "docker",
      ["inspect", config.composeService, "--format", "{{.Image}}"],
      { timeout: GIT_TIMEOUT_MS }
    );
    if (builtId.trim() !== runningId.trim()) {
      throw new Error("recreated container is not running the freshly built image");
    }
  } finally {
    await rm(checkout, { recursive: true, force: true }).catch(() => undefined);
  }
}
