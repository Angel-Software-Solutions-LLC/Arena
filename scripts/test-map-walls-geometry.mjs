import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  WALL_HEIGHT,
  RESAMPLE_SPACING,
  SMOOTH_SIGMA,
  CORNER_TURN_DEG,
  MAX_SMOOTH_DEVIATION,
  inferCellSize,
  rasterizeMask,
  extractContours,
  mergeCollinear,
  resampleClosed,
  detectCorners,
  gaussianSmoothClosed,
  smoothContour,
  contourArea,
  buildBoundaryContours,
  buildWallGeometry,
  buildTrimGeometry,
  MapWallsRenderer,
} from '../frontend/js/renderer/map-walls.js';

// --- module identity: the wall height must match the pillar height the
// boundary boxes used, and the importer chain must carry the new stamp.
const obstaclesSource = readFileSync(new URL('../frontend/js/renderer/obstacles.js', import.meta.url), 'utf8');
const engineSource = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');
const settingsSource = readFileSync(new URL('../frontend/js/settings.js', import.meta.url), 'utf8');
const pillarMatch = obstaclesSource.match(/const PILLAR_HEIGHT = (\d+);/);
assert.ok(pillarMatch, 'obstacles.js must declare PILLAR_HEIGHT');
assert.equal(WALL_HEIGHT, Number(pillarMatch[1]),
  'smooth boundary walls must match the boundary-box pillar height');
assert.match(obstaclesSource, /map-walls\.js\?v=20260718g/,
  'obstacles.js must import the stamped map-walls module');
assert.match(engineSource, /obstacles\.js\?v=20260903a/,
  'engine.js must invalidate the cached pre-#190 obstacle renderer');
assert.match(engineSource, /state\.mask_rects/,
  'engine.js must feed mask_rects from spectator keyframes');
assert.match(settingsSource, /smoothMapWalls/,
  'the smoothMapWalls toggle must exist in SETTINGS_SCHEMA');

// --- smoothing contract constants (issue #190)
assert.equal(RESAMPLE_SPACING, 0.5, 'contours resample at half-cell arc spacing');
assert.equal(SMOOTH_SIGMA, 1.0, 'the Gaussian blur runs at a one-cell sigma');
assert.equal(CORNER_TURN_DEG, 55, 'corners pin at a 55-degree windowed net turn');
assert.equal(MAX_SMOOTH_DEVIATION, 0.5,
  'no smoothed vertex may leave the half-cell band around the collision staircase');

// --- cell size inference: gcd of grid-aligned rects, clamped, defaulted.
assert.equal(inferCellSize([{ x: 40, y: 60, width: 20, height: 100 }]), 20);
assert.equal(inferCellSize([{ x: 80, y: 0, width: 40, height: 120 }]), 40);
assert.equal(inferCellSize([{ x: 0, y: 0, width: 7, height: 13 }]), 20,
  'non-grid rects must fall back to the config default cell size');
assert.equal(inferCellSize([]), 20);

// --- single rect: rasterize + trace yields one rectangle contour.
{
  const mask = rasterizeMask([{ x: 0, y: 0, width: 60, height: 40 }], 20);
  assert.equal(mask.cols, 3);
  assert.equal(mask.rows, 2);
  const contours = extractContours(mask);
  assert.equal(contours.length, 1, 'one rect must trace to one closed contour');
  const merged = mergeCollinear(contours[0]);
  assert.equal(merged.length, 4, 'a rectangle must merge to 4 corners');
  assert.ok(contourArea(merged) > 0, 'blocked-region outlines must have positive area');
  assert.equal(contourArea(merged), 6, 'shoelace area must equal the cell count');
}

// --- collinear merge drops redundant lattice points only.
{
  const square = [[0, 0], [1, 0], [2, 0], [2, 1], [2, 2], [1, 2], [0, 2], [0, 1]];
  assert.deepEqual(mergeCollinear(square), [[0, 0], [2, 0], [2, 2], [0, 2]]);
}

// --- resampling: on a lattice polygon the perimeter is an integer, so the
// spacing is EXACTLY the contract value and every lattice corner is a sample.
{
  const square = [[0, 0], [12, 0], [12, 12], [0, 12]];
  const samples = resampleClosed(square);
  assert.equal(samples.length, 96, 'perimeter 48 / 0.5 spacing → 96 samples');
  for (let i = 0; i < samples.length; i++) {
    const [x0, y0] = samples[i];
    const [x1, y1] = samples[(i + 1) % samples.length];
    assert.ok(Math.abs(Math.hypot(x1 - x0, y1 - y0) - RESAMPLE_SPACING) < 1e-9,
      'samples must be uniformly spaced at the contract spacing');
  }
  for (const corner of square) {
    assert.ok(samples.some(([x, y]) => Math.hypot(x - corner[0], y - corner[1]) < 1e-9),
      'every lattice corner must land exactly on a sample');
  }
}

const distToPolyline = (p, pts, closed = true) => {
  const n = pts.length;
  let best = Infinity;
  const end = closed ? n : n - 1;
  for (let i = 0; i < end; i++) {
    const [x0, y0] = pts[i];
    const [x1, y1] = pts[(i + 1) % n];
    const dx = x1 - x0;
    const dy = y1 - y0;
    const t = Math.max(0, Math.min(1, ((p[0] - x0) * dx + (p[1] - y0) * dy) / (dx * dx + dy * dy || 1)));
    best = Math.min(best, Math.hypot(p[0] - (x0 + t * dx), p[1] - (y0 + t * dy)));
  }
  return best;
};

// --- corner preservation on a crisp rectilinear cross: all 12 genuine
// corners are detected and pinned, and every smoothed point stays ON the
// original outline (straight runs of a rectilinear shape never bow).
{
  const cross = [
    [6, 0], [12, 0], [12, 6], [18, 6], [18, 12], [12, 12],
    [12, 18], [6, 18], [6, 12], [0, 12], [0, 6], [6, 6],
  ];
  const samples = resampleClosed(cross);
  const corners = detectCorners(samples);
  assert.equal(corners.length, 12, 'every 90-degree corner of the cross must pin');
  const smoothed = smoothContour(cross);
  for (const c of cross) {
    assert.ok(smoothed.some(([x, y]) => Math.hypot(x - c[0], y - c[1]) < 1e-9),
      `corner ${c} must survive smoothing exactly`);
  }
  for (const p of smoothed) {
    assert.ok(distToPolyline(p, cross) < 1e-9,
      'rectilinear outlines must pass through the smoother unchanged');
  }
}

// --- deviation clamp: on a thin sliver the end caps would pull inward by
// over a cell (the blur window folds around the tip); the clamp must cap
// EVERY vertex at half a cell from its staircase position, and the fixture
// must actually engage it.
{
  const sliver = [[0, 0], [20, 0], [20, 1], [0, 1]];
  const samples = resampleClosed(sliver);
  const smoothed = gaussianSmoothClosed(samples, []);
  let engaged = false;
  for (let i = 0; i < samples.length; i++) {
    const d = Math.hypot(smoothed[i][0] - samples[i][0], smoothed[i][1] - samples[i][1]);
    assert.ok(d <= MAX_SMOOTH_DEVIATION + 1e-9,
      `every vertex must stay within the deviation clamp (moved ${d})`);
    if (d > MAX_SMOOTH_DEVIATION - 1e-6) engaged = true;
  }
  assert.ok(engaged, 'the sliver fixture must actually engage the clamp');
}

/** Blocked-apron fixture builder: rects covering every cell of an NxN grid
 *  where isOpen(x, y) is false, as row runs (mirrors the server's
 *  MaskToRects output shape). */
function apronRects(N, cell, isOpen) {
  const rects = [];
  let openCells = 0;
  for (let y = 0; y < N; y++) {
    let x = 0;
    while (x < N) {
      if (isOpen(x, y)) { openCells++; x++; continue; }
      const x0 = x;
      while (x < N && !isOpen(x, y)) x++;
      rects.push({ x: x0 * cell, y: y * cell, width: (x - x0) * cell, height: cell });
    }
  }
  return { rects, openCells };
}

// --- circle fixture (the scallop regression, issue #190): a rasterized
// 10-cell-radius disk must come out visually ROUND — no periodic Chaikin
// scallops. Roundness: max radial deviation from the mean radius well
// under half a cell; no false corners pin on the curve; and every point
// stays within the deviation clamp of the collision staircase.
{
  const cell = 20;
  const N = 24;
  const R = 10;
  const { rects } = apronRects(N, cell, (x, y) => (x - 11.5) ** 2 + (y - 11.5) ** 2 <= R * R);
  const { cellSize, groups } = buildBoundaryContours(rects);
  assert.equal(cellSize, 20);
  assert.equal(groups.length, 1, 'the apron must form exactly one region');
  const [group] = groups;
  assert.equal(group.outer.length, 4, 'the outer rectangle must stay crisp (unsmoothed)');
  assert.equal(group.holes.length, 1, 'the playfield disk must be the inner hole');
  const hole = group.holes[0];
  assert.ok(contourArea(hole) < 0, 'holes must wind opposite to outers');

  const cx = 12 * cell;
  const cy = 12 * cell;
  const radii = hole.map(([x, y]) => Math.hypot(x - cx, y - cy));
  const mean = radii.reduce((a, b) => a + b, 0) / radii.length;
  let maxDev = 0;
  for (const r of radii) maxDev = Math.max(maxDev, Math.abs(r - mean));
  assert.ok(maxDev <= 0.3 * cell,
    `a rasterized circle must smooth visually round (max radial dev ${(maxDev / cell).toFixed(3)} cells; measured 0.238 at authoring time)`);

  // No corner may pin on a smooth curve of this radius.
  const mask = rasterizeMask(rects, cell);
  const rawHole = extractContours(mask).map(mergeCollinear).find((c) => contourArea(c) < 0);
  assert.ok(rawHole, 'the raw staircase hole must exist');
  assert.equal(detectCorners(resampleClosed(rawHole)).length, 0,
    'a 10-cell-radius circle must not trigger corner pinning');

  // Deviation clamp against the collision staircase (world units).
  const rawWorld = rawHole.map(([x, y]) => [x * cell + mask.originX, y * cell + mask.originY]);
  for (const p of hole) {
    assert.ok(distToPolyline(p, rawWorld) <= MAX_SMOOTH_DEVIATION * cell + 1e-6,
      'smoothed circle points must stay within half a cell of the staircase');
  }
}

// --- hexagon: 60-degree vertices are the shallowest genuine corners in
// the shape roster and must still pin (6 on the server's flat-top mask).
{
  const cell = 20;
  const N = 60;
  const c = (N - 1) / 2;
  const { rects } = apronRects(N, cell, (x, y) => {
    const dx = Math.abs(x - c) / c;
    const dy = Math.abs(y - c) / c;
    return dy <= 0.866 && 0.866 * dx + 0.5 * dy <= 0.866;
  });
  const mask = rasterizeMask(rects, cell);
  const rawHole = extractContours(mask).map(mergeCollinear).find((k) => contourArea(k) < 0);
  assert.equal(detectCorners(resampleClosed(rawHole)).length, 6,
    'hexagon vertices (60 deg) must pin as corners');
}

// --- diamond fixture: an 8-cell-radius diamond keeps 4 crisp corners at
// its exact staircase tips, with straight edges between them, while the
// apron's outer rectangle stays a crisp 4-corner box. Also exercises the
// wall/trim geometry builders and the 2-mesh renderer contract below.
{
  const cell = 20;
  const N = 19;
  const inDiamond = (x, y) => Math.abs(x - 9) + Math.abs(y - 9) <= 8;
  const { rects, openCells } = apronRects(N, cell, inDiamond);
  const { cellSize, groups } = buildBoundaryContours(rects);
  assert.equal(cellSize, 20);
  assert.equal(groups.length, 1, 'the apron must form exactly one region');
  const [group] = groups;
  assert.equal(group.outer.length, 4, 'the outer rectangle must stay crisp (unsmoothed)');
  const xs = group.outer.map((p) => p[0]);
  const zs = group.outer.map((p) => p[1]);
  assert.equal(Math.min(...xs), 0);
  assert.equal(Math.max(...xs), N * cell);
  assert.equal(Math.min(...zs), 0);
  assert.equal(Math.max(...zs), N * cell);
  assert.equal(group.holes.length, 1, 'the playfield outline must be the inner hole');
  const hole = group.holes[0];
  assert.ok(hole.length >= 16, 'the staircase hole must have been resampled densely');
  assert.ok(contourArea(hole) < 0, 'holes must wind opposite to outers');
  const holeArea = Math.abs(contourArea(hole));
  const openArea = openCells * cell * cell;
  assert.ok(holeArea >= 0.9 * openArea && holeArea <= 1.05 * openArea,
    `midline smoothing must preserve the playfield area (${holeArea} vs ${openArea})`);

  // The 4 diamond tips sit mid-edge on their tip cells; the smoother must
  // pin a vertex EXACTLY there (half-cell resampling lands on them).
  const tips = [[1, 9.5], [18, 9.5], [9.5, 1], [9.5, 18]].map(([x, y]) => [x * cell, y * cell]);
  const tipIdx = [];
  for (const [tx, ty] of tips) {
    let best = Infinity;
    let bestI = -1;
    hole.forEach(([x, y], i) => {
      const d = Math.hypot(x - tx, y - ty);
      if (d < best) { best = d; bestI = i; }
    });
    assert.ok(best < 1e-6, `a crisp vertex must sit exactly on the diamond tip (${best})`);
    tipIdx.push(bestI);
  }
  // Crisp corner: the chords 2 cells to either side of a tip meet at ~90.
  for (const i of tipIdx) {
    const n = hole.length;
    const a = hole[(i - 4 + n) % n];
    const b = hole[i];
    const c = hole[(i + 4) % n];
    const ang = Math.abs(Math.atan2(
      (b[0] - a[0]) * (c[1] - b[1]) - (b[1] - a[1]) * (c[0] - b[0]),
      (b[0] - a[0]) * (c[0] - b[0]) + (b[1] - a[1]) * (c[1] - b[1]),
    )) * (180 / Math.PI);
    assert.ok(ang > 70 && ang < 110, `diamond tips must stay crisp (~90 deg turn, got ${ang.toFixed(1)})`);
  }
  // Straight edges: every hole point hugs the ideal diamond outline.
  const order = [tips[0], tips[2], tips[1], tips[3]];
  let worstEdge = 0;
  for (const p of hole) {
    worstEdge = Math.max(worstEdge, distToPolyline(p, order));
  }
  assert.ok(worstEdge <= 0.25 * cell,
    `diamond edges must smooth straight (max ${(worstEdge / cell).toFixed(3)} cells off the ideal edge; measured 0.048 at authoring time)`);

  // Wall geometry (ring-cap fallback path — used only if the earcut CDN
  // script failed): valid indexed triangles, every vertex at floor height,
  // wall height, or the trim band around it.
  const wall = buildWallGeometry(groups, { height: WALL_HEIGHT, capWidth: cellSize * 2, earcutFn: null });
  assert.equal(wall.positions.length % 3, 0);
  assert.equal(wall.positions.length, wall.normals.length);
  assert.equal(wall.indices.length % 3, 0);
  const vertexCount = wall.positions.length / 3;
  for (const idx of wall.indices) {
    assert.ok(Number.isInteger(idx) && idx >= 0 && idx < vertexCount, 'indices must address real vertices');
  }
  for (let i = 1; i < wall.positions.length; i += 3) {
    const y = wall.positions[i];
    assert.ok(y === 0 || y === WALL_HEIGHT, `wall vertices must sit at floor or cap height (got ${y})`);
  }

  // Full-cap path with an earcut stand-in still emits valid geometry:
  // 2 side vertices per contour point plus 1 flat-cap vertex per point.
  const earcutStub = (flat) => (flat.length >= 6 ? [0, 1, 2] : []);
  const capped = buildWallGeometry(groups, { height: WALL_HEIGHT, earcutFn: earcutStub });
  assert.ok(capped.indices.length > 0);
  const totalPts = group.outer.length + hole.length;
  assert.equal(capped.positions.length / 3, totalPts * 3,
    'the earcut path must add one cap vertex per contour point');

  const trim = buildTrimGeometry(groups, { height: WALL_HEIGHT });
  assert.equal(trim.positions.length % 3, 0);
  assert.equal(trim.positions.length, trim.normals.length);
  const trimVertexCount = trim.positions.length / 3;
  for (const idx of trim.indices) {
    assert.ok(Number.isInteger(idx) && idx >= 0 && idx < trimVertexCount);
  }
  for (let i = 1; i < trim.positions.length; i += 3) {
    const y = trim.positions[i];
    assert.ok(y > WALL_HEIGHT - 3 && y < WALL_HEIGHT + 3,
      `trim vertices must hug the wall top edge (got ${y})`);
  }

  // Renderer contract with a stubbed BABYLON: the whole boundary is exactly
  // two meshes (body + trim), the body is a shadow caster excluded from the
  // glow layer, and a rebuild disposes the previous pair.
  const created = [];
  class FakeMesh {
    constructor(name) { this.name = name; this.disposed = false; created.push(this); }
    dispose() { this.disposed = true; }
    freezeWorldMatrix() { this.frozen = true; }
  }
  class FakeVertexData {
    applyToMesh(mesh) { mesh.vertexData = this; }
  }
  globalThis.window = { BABYLON: { Mesh: FakeMesh, VertexData: FakeVertexData } };
  const casters = [];
  const env = { addShadowCaster: (m) => casters.push(m), refreshShadows: () => {} };
  const excluded = new Set();
  const glow = {
    addExcludedMesh: (m) => excluded.add(m),
    removeExcludedMesh: (m) => excluded.delete(m),
  };
  const trimSource = {
    emissiveColor: { copyFrom() {} },
    clone: () => ({ backFaceCulling: true, freeze() {}, unfreeze() {}, emissiveColor: { copyFrom() {} } }),
  };
  const walls = new MapWallsRenderer({}, env);
  walls.setGlowLayer(glow);
  walls.build(rects, { name: 'bodyMat' }, trimSource);
  assert.equal(created.length, 2, 'the boundary must build exactly two meshes (body + trim)');
  const [bodyMesh, trimMesh] = created;
  assert.equal(bodyMesh.name, 'mapWallBody');
  assert.equal(trimMesh.name, 'mapWallTrim');
  assert.ok(bodyMesh.frozen && trimMesh.frozen, 'wall meshes must freeze their world matrices');
  assert.deepEqual(casters, [bodyMesh], 'only the wall body casts shadows');
  assert.ok(excluded.has(bodyMesh), 'the wall body must not feed the glow pass');
  assert.equal(trimMesh.material.backFaceCulling, false, 'the trim ribbon must render both faces');
  walls.build(rects, { name: 'bodyMat' }, trimSource);
  assert.ok(bodyMesh.disposed && trimMesh.disposed, 'a rebuild must dispose the previous meshes');
  assert.ok(!excluded.has(bodyMesh), 'disposing the body must drop its glow exclusion');
  assert.equal(created.length, 4);
  walls.clear();
  assert.ok(created[2].disposed && created[3].disposed);
  delete globalThis.window;
}

console.log('map-walls geometry: resampled Gaussian smoothing (round circles, crisp corners, half-cell clamp) and the 2-mesh boundary contract hold');
