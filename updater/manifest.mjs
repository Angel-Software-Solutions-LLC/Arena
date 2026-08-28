// Release-manifest bookkeeping for the self-updater's file sync.
//
// The sync deliberately never uses rsync --delete: the deploy directory
// legitimately holds VPS-only files (docker-compose.override.yml, .env)
// that a tree-diff delete would destroy — which is exactly how the first
// live use of --delete broke a build. But never deleting anything has the
// opposite failure: a file REMOVED from the repo lingers in the deploy
// directory forever, and the first upstream refactor that deleted a Go
// file left both the old and new files in one package — duplicate
// declarations, broken build (2026-07-14).
//
// The manifest resolves both failure modes. Every applied release records
// the exact file list of its fetched tree. On the next update, files that
// were in the PREVIOUS release's manifest but absent from the NEW tree are
// repo-owned deletions and are removed; files the repo never shipped
// (operator config, secrets) are never in a manifest, so they can never be
// touched. The first update after this feature ships has no manifest and
// deletes nothing.

import { readdir, readFile, writeFile, unlink } from "node:fs/promises";
import { join, sep } from "node:path";

export const MANIFEST_FILENAME = ".arena-release-manifest.json";

/** Recursively list all regular files under rootDir as sorted, /-separated
 * relative paths. Symlinks never appear: the updater fails the whole update
 * on any symlink before this runs. */
export async function listReleaseFiles(rootDir) {
  const entries = await readdir(rootDir, { recursive: true, withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    const parent = entry.parentPath ?? entry.path;
    const absolute = join(parent, entry.name);
    const relative = absolute.slice(rootDir.length + 1).split(sep).join("/");
    files.push(relative);
  }
  files.sort();
  return files;
}

/** A manifest path is only ever consumed if it is a plain relative path that
 * cannot escape the deploy directory and does not reach into an excluded
 * tree (.git, .env) even if a hand-edited manifest claims otherwise. */
export function isSafeManifestPath(path, excludedPrefixes) {
  if (typeof path !== "string" || path.length === 0 || path.length > 4096) return false;
  if (path.startsWith("/") || path.includes("\\") || /^[a-zA-Z]:/.test(path)) return false;
  const segments = path.split("/");
  if (segments.some((segment) => segment === "" || segment === "." || segment === "..")) return false;
  for (const prefix of excludedPrefixes) {
    const clean = prefix.replace(/^\/+/, "");
    if (path === clean || path.startsWith(`${clean}/`)) return false;
  }
  return true;
}

/** Files present in the previous release but absent from the new one:
 * repo-owned deletions the sync must apply. */
export function vanishedFiles(previousFiles, currentFiles, excludedPrefixes) {
  const current = new Set(currentFiles);
  const vanished = [];
  for (const path of previousFiles) {
    if (current.has(path)) continue;
    if (!isSafeManifestPath(path, excludedPrefixes)) continue;
    vanished.push(path);
  }
  return vanished;
}

/** Read the previous release manifest, tolerating a missing or corrupt file
 * (both mean "delete nothing this round"). */
export async function readManifest(deployDir) {
  try {
    const raw = await readFile(join(deployDir, MANIFEST_FILENAME), "utf8");
    const parsed = JSON.parse(raw);
    if (!parsed || !Array.isArray(parsed.files)) return null;
    return parsed.files.filter((entry) => typeof entry === "string");
  } catch {
    return null;
  }
}

export async function writeManifest(deployDir, commitSha, files) {
  const payload = JSON.stringify({ commit: commitSha, files }, null, 0);
  await writeFile(join(deployDir, MANIFEST_FILENAME), payload, "utf8");
}

/** Delete repo-owned files that vanished from the new release. Missing files
 * are fine (already gone); anything else propagates so the update surfaces
 * the failure instead of building a half-synced tree. Returns the paths it
 * removed. */
export async function removeVanishedFiles(deployDir, previousFiles, currentFiles, excludedPrefixes) {
  const removed = [];
  for (const path of vanishedFiles(previousFiles, currentFiles, excludedPrefixes)) {
    try {
      await unlink(join(deployDir, path.split("/").join(sep)));
      removed.push(path);
    } catch (error) {
      if (error?.code === "ENOENT") continue;
      if (error?.code === "EISDIR" || error?.code === "EPERM") continue;
      throw error;
    }
  }
  return removed;
}
