'use strict';

/**
 * Obstacle rendering — stone pillars/walls with emissive edges.
 * Per-map palettes and rooftop detailing (issue #182) are applied at the
 * round-boundary rebuild. Carved map-boundary rects (issue #186) are split
 * out of the box build and rendered by MapWallsRenderer as one smoothed
 * contour wall (two draw calls: wall body + glow trim) when the server
 * sends `mask_rects` and the arenaAmbience.smoothMapWalls setting is on.
 *
 * Touching/overlapping rects (issue #190) are grouped into connected
 * clusters and each multi-rect cluster renders as ONE prism extruded from
 * the exact rectilinear union outline with a SINGLE continuous trim — the
 * old per-rect build put a +1.5-oversized translucent trim box on every
 * rect, and wherever rects abutted a trim crossed the neighbor's body
 * (bright patches + seam lines). Isolated rects keep the cheap box path.
 * Draw calls stay bounded: merged box bodies + merged box trims (as
 * before) plus at most one merged cluster-prism body and one merged
 * cluster trim. Cluster unification needs earcut for the prism caps; if
 * the CDN script failed to load, everything falls back to the pre-#190
 * box build rather than shipping open-topped prisms.
 * @module renderer/obstacles
 */

import { isEnabled } from '../settings.js';
import { MapWallsRenderer, buildWallGeometry, resolveEarcut } from './map-walls.js?v=20260718g';
import { clusterObstacles, computeClusterOutline, buildClusterTrimGeometry } from './obstacle-clusters.js?v=20260718g';


/**
 * Mark a mesh as permanently static.
 *
 * freezeWorldMatrix alone still leaves Babylon syncing bounding info and
 * running the full bounding-box culling test for the mesh every frame. These
 * meshes never move, never deform, and are never picked (nothing in the
 * frontend calls scene.pick), so both are wasted per-frame CPU.
 *
 * Geometry is not this scene's bottleneck -- a measured idle scene is ~28
 * active meshes against ~253 draw calls, which are dominated by the
 * post-process chain and particle systems -- so this is a modest, safe
 * saving rather than a structural one.
 *
 * @param {object} mesh Babylon mesh that will never move again
 */
function markStaticMesh(mesh) {
  if (!mesh) return;
  mesh.freezeWorldMatrix();
  mesh.doNotSyncBoundingInfo = true;
  const B = window.BABYLON;
  if (B && B.AbstractMesh && B.AbstractMesh.CULLINGSTRATEGY_BOUNDINGSPHERE_ONLY !== undefined) {
    mesh.cullingStrategy = B.AbstractMesh.CULLINGSTRATEGY_BOUNDINGSPHERE_ONLY;
  }
}

export const PILLAR_HEIGHT = 30;

/**
 * Split obstacle rects into isolated boxes and multi-rect cluster unions
 * (issue #190), exactly as the round build renders them. Without an earcut
 * triangulator everything degrades to isolated boxes (prism caps need it).
 * Exported so the intermission construction pre-build (issue #192) composes
 * the IDENTICAL layout — same clustering, same isolated ordering (the
 * rooftop-detail hash indexes depend on it).
 * @param {Array<{x:number,y:number,width:number,height:number}>} buildObstacles
 * @param {Function|null} earcutFn
 * @returns {{isolated:Array, unions:Array<{groups:Array,rects:Array}>}}
 */
export function composeObstacleLayout(buildObstacles, earcutFn) {
  const isolated = [];
  const unions = [];
  if (earcutFn) {
    for (const memberIdxs of clusterObstacles(buildObstacles)) {
      if (memberIdxs.length === 1) {
        isolated.push(buildObstacles[memberIdxs[0]]);
        continue;
      }
      const rects = memberIdxs.map((i) => buildObstacles[i]);
      const outline = computeClusterOutline(rects);
      if (outline) unions.push({ groups: outline.groups, rects });
      else isolated.push(...rects);
    }
  } else {
    isolated.push(...buildObstacles);
  }
  return { isolated, unions };
}

/**
 * Append the box-path meshes for one isolated obstacle — stone pillar body
 * plus the oversized top-edge and base glow trims — positioned exactly as
 * the merged round build places them. Shared with the intermission
 * construction pre-build (issue #192) so the transient rise is
 * geometry-identical to the real handoff build.
 */
export function appendObstacleBoxes(scene, obs, i, bodyBoxes, trimBoxes) {
  const B = window.BABYLON;
  // Stone pillar
  const mesh = B.MeshBuilder.CreateBox(`obs-${i}`, {
    width: obs.width, height: PILLAR_HEIGHT, depth: obs.height
  }, scene);
  mesh.position.set(obs.x + obs.width / 2, PILLAR_HEIGHT / 2, obs.y + obs.height / 2);
  bodyBoxes.push(mesh);

  // Glowing edge wireframe on top
  const edge = B.MeshBuilder.CreateBox(`obsEdge-${i}`, {
    width: obs.width + 1.5, height: 2, depth: obs.height + 1.5
  }, scene);
  edge.position.set(obs.x + obs.width / 2, PILLAR_HEIGHT + 0.8, obs.y + obs.height / 2);
  trimBoxes.push(edge);

  // Bottom glow ring
  const base = B.MeshBuilder.CreateBox(`obsBase-${i}`, {
    width: obs.width + 1.5, height: 1.5, depth: obs.height + 1.5
  }, scene);
  base.position.set(obs.x + obs.width / 2, 0.75, obs.y + obs.height / 2);
  trimBoxes.push(base);
}

/**
 * Rooftop detailing (issue #182c): a raised inset panel, with a small
 * glowing stud on every third detail index, so structures read as
 * architecture instead of extruded rectangles. Variation is derived purely
 * from the detail index (golden-ratio hash) — rebuilding the same layout
 * always produces the same roofs, no Math.random drift. Panels share the
 * body material and studs the trim material, so both merge into the
 * existing box draw calls. Shared with the intermission pre-build (#192).
 */
export function appendRoofDetail(scene, obs, i, bodyBoxes, trimBoxes) {
  const B = window.BABYLON;
  const hash = (i * 0.61803398875) % 1;
  const inset = 0.52 + hash * 0.3;
  const raise = 0.9 + (i % 3) * 0.5;
  const panel = B.MeshBuilder.CreateBox(`obsTop-${i}`, {
    width: Math.max(2, obs.width * inset),
    height: raise,
    depth: Math.max(2, obs.height * inset),
  }, scene);
  panel.position.set(obs.x + obs.width / 2, PILLAR_HEIGHT + raise / 2, obs.y + obs.height / 2);
  bodyBoxes.push(panel);

  if (i % 3 === 0) {
    const stud = B.MeshBuilder.CreateBox(`obsStud-${i}`, {
      width: Math.max(1.2, obs.width * 0.18),
      height: 0.9,
      depth: Math.max(1.2, obs.height * 0.18),
    }, scene);
    stud.position.set(
      obs.x + obs.width / 2 + ((i % 4) < 2 ? -1 : 1) * obs.width * inset * 0.22,
      PILLAR_HEIGHT + raise + 0.45,
      obs.y + obs.height / 2 + (i % 2 ? -1 : 1) * obs.height * inset * 0.22,
    );
    trimBoxes.push(stud);
  }
}

export class ObstacleRenderer {
  /** @param {BABYLON.Scene} scene @param {EnvironmentRenderer} [envRenderer] */
  constructor(scene, envRenderer) {
    this.scene = scene;
    this._env = envRenderer || null;
    /** @type {BABYLON.Mesh|null} isolated pillar bodies merged (one draw call) */
    this._bodyMesh = null;
    /** @type {BABYLON.Mesh|null} isolated edge + base trims merged (one draw call) */
    this._trimMesh = null;
    /** @type {BABYLON.Mesh|null} all cluster union prisms merged (one draw call) */
    this._clusterBodyMesh = null;
    /** @type {BABYLON.Mesh|null} all cluster union trims merged (one draw call) */
    this._clusterTrimMesh = null;
    this._mat = null;
    this._edgeMat = null;
    this._clusterTrimMat = null;
    this._lastObstacles = null;
    /** @type {Array|null} carved map-boundary rects from the last keyframe */
    this._lastMaskRects = null;
    // Smooth boundary walls (issue #186) ride the same round-boundary
    // rebuild as the merged pillars, sharing this renderer's materials.
    this._mapWalls = new MapWallsRenderer(scene, envRenderer);
    this._initMaterials();
  }

  /** Wire the engine's GlowLayer through to the boundary-wall builder so
   *  the wall body can be excluded from glow (only its trim glows, #181). */
  setGlowLayer(glow) {
    this._mapWalls.setGlowLayer(glow);
  }

  /** @private Create shared materials (default-palette hues; retinted per map). */
  _initMaterials() {
    const B = window.BABYLON;
    // Dark alloy body with restrained cool highlights.
    this._mat = new B.StandardMaterial('obsMat', this.scene);
    this._mat.diffuseColor = new B.Color3(0.07, 0.085, 0.11);
    this._mat.emissiveColor = new B.Color3(0.015, 0.03, 0.05);
    this._mat.specularColor = new B.Color3(0.12, 0.16, 0.24);
    this._mat.specularPower = 96;
    this._mat.backFaceCulling = false;
    this._mat.freeze();

    // Cleaner edge accent, less debug-neon.
    this._edgeMat = new B.StandardMaterial('obsEdgeMat', this.scene);
    this._edgeMat.diffuseColor = B.Color3.Black();
    this._edgeMat.emissiveColor = new B.Color3(0.08, 0.34, 0.62);
    this._edgeMat.disableLighting = true;
    this._edgeMat.alpha = 0.58;
    this._edgeMat.freeze();
  }

  /** @private Retint the shared materials from the environment's map palette. */
  _applyPalette() {
    const palette = this._env && this._env.getPalette ? this._env.getPalette() : null;
    if (!palette) return;
    this._mat.unfreeze();
    this._mat.diffuseColor.set(...palette.obstacleBody.diffuse);
    this._mat.emissiveColor.set(...palette.obstacleBody.emissive);
    this._mat.freeze();
    this._edgeMat.unfreeze();
    this._edgeMat.emissiveColor.set(...palette.obstacleTrim);
    this._edgeMat.freeze();
  }

  /** @private Culling-disabled clone of the shared trim material for the
   *  cluster trim bands (open ring geometry with single-layer caps — same
   *  pattern as MapWallsRenderer's trim clone). Retinted on every rebuild
   *  so map palettes from #182 keep applying. */
  _syncClusterTrimMat() {
    if (!this._clusterTrimMat) {
      this._clusterTrimMat = this._edgeMat.clone('obsClusterTrimMat');
      this._clusterTrimMat.unfreeze();
      this._clusterTrimMat.backFaceCulling = false;
    } else {
      this._clusterTrimMat.unfreeze();
      this._clusterTrimMat.emissiveColor.copyFrom(this._edgeMat.emissiveColor);
    }
    this._clusterTrimMat.freeze();
    return this._clusterTrimMat;
  }

  /**
   * Update obstacles from state.
   * @param {Array} obstacles - [{ x, y, width, height }]
   * @param {Array} [maskRects] - carved boundary rects (subset of obstacles),
   *   present only on keyframes from servers that itemize them (issue #186)
   */
  update(obstacles, maskRects) {
    if (!obstacles) return;
    const B = window.BABYLON;
    this._lastObstacles = obstacles;
    // Obstacles only arrive on keyframes and mask_rects rides the same
    // keyframes, so whenever obstacles are present the field is
    // authoritative — absent means square map or a pre-#186 server.
    this._lastMaskRects = maskRects && maskRects.length ? maskRects : null;

    // Smooth boundary walls (issue #186): when the server itemizes the
    // carved boundary rects, render them as one smoothed contour wall and
    // keep the per-cell pillar/trim build for the real obstacles only.
    // Setting off or no mask_rects → everything renders as boxes as before.
    const smoothWalls = !!this._lastMaskRects && isEnabled('arenaAmbience', 'smoothMapWalls');

    // Detect if obstacles changed (new round). Build a fingerprint from
    // the obstacle data to compare against last update. (Mask rects are a
    // verbatim subset of the obstacle list, so they can't change alone.)
    const fp = obstacles.map(o => `${o.x},${o.y},${o.width},${o.height}`).join('|') +
      (smoothWalls ? `#walls${this._lastMaskRects.length}` : '');
    if (fp === this._lastFingerprint) return; // same layout — merged meshes stand

    // Obstacles changed — dispose the old merged meshes and rebuild from
    // scratch. This handles frozen world matrices and size changes between
    // rounds. The palette retint and the environment's floor re-bake
    // (contact shadows) ride the same round-boundary trigger.
    this.clear();
    this._lastFingerprint = fp;
    this._applyPalette();

    // Boundary rects leave the pillar build, the rooftop detailing, and the
    // contact-shadow floor bake — they're map boundary, not architecture.
    // Matched by exact x/y/width/height key against the mask_rects list.
    let buildObstacles = obstacles;
    if (smoothWalls) {
      const maskKeys = new Set(this._lastMaskRects.map(o => `${o.x},${o.y},${o.width},${o.height}`));
      buildObstacles = obstacles.filter(o => !maskKeys.has(`${o.x},${o.y},${o.width},${o.height}`));
    }

    // Cluster grouping (issue #190): multi-rect clusters become one union
    // prism each; everything else stays on the box path. A cluster whose
    // rects are not grid-aligned (or absurdly large) degrades to boxes too.
    const earcutFn = resolveEarcut();
    const { isolated, unions } = composeObstacleLayout(buildObstacles, earcutFn);

    // Contact shadows follow the clusters: isolated rects bake as rects,
    // each union as one polygon along its exact outline (issue #190).
    if (this._env && this._env.setRoundObstacles) {
      this._env.setRoundObstacles(isolated, unions.flatMap((u) => u.groups));
    }
    // Built before the shadow re-bake below so the wall body is registered
    // as a caster in time; materials were just palette-tinted above.
    this._mapWalls.build(smoothWalls ? this._lastMaskRects : null, this._mat, this._edgeMat);
    if (!buildObstacles.length) {
      // Casters were just removed — the frozen shadow map still needs the
      // re-bake or it would keep showing the previous round's shadows.
      if (this._env && this._env.refreshShadows) this._env.refreshShadows();
      return;
    }

    // Build the boxes per isolated obstacle as before, merged into two
    // meshes — one per shared material. The layout is immutable for the
    // whole round (the fingerprint proves it), so 3-5 draw calls per
    // obstacle collapse to 2 total. One merge call per material group means
    // no multi-materials are needed.
    const detailing = isEnabled('arenaAmbience', 'obstacleDetailing');
    const bodyBoxes = [];
    const trimBoxes = [];
    isolated.forEach((obs, i) => {
      appendObstacleBoxes(this.scene, obs, i, bodyBoxes, trimBoxes);
      if (detailing) appendRoofDetail(this.scene, obs, i, bodyBoxes, trimBoxes);
    });

    // Rooftop detailing follows the clusters (issue #190): ONE feature per
    // cluster, seated on its largest member rect, hashed by a detail index
    // that continues past the isolated rects — deterministic across
    // rebuilds, and no per-rect repetition inside a unified structure.
    if (detailing) {
      unions.forEach((u, ci) => {
        const seat = u.rects.reduce((best, r) =>
          r.width * r.height > best.width * best.height ? r : best);
        appendRoofDetail(this.scene, seat, isolated.length + ci, bodyBoxes, trimBoxes);
      });
    }

    this._buildClusterMeshes(unions, earcutFn);

    // MergeMeshes(meshes, disposeSource=true, allow32BitsIndices=true) —
    // sources are disposed inside the merge, so nothing leaks here.
    if (bodyBoxes.length) {
      this._bodyMesh = B.Mesh.MergeMeshes(bodyBoxes, true, true);
    }
    if (this._bodyMesh) {
      this._bodyMesh.name = 'obstacleBodies';
      this._bodyMesh.material = this._mat;
      this._bodyMesh.isPickable = false;
      markStaticMesh(this._bodyMesh);
      // Only the opaque bodies cast — the trims are emissive decoration.
      if (this._env) this._env.addShadowCaster(this._bodyMesh);
    }
    if (trimBoxes.length) {
      this._trimMesh = B.Mesh.MergeMeshes(trimBoxes, true, true);
    }
    if (this._trimMesh) {
      this._trimMesh.name = 'obstacleTrims';
      this._trimMesh.material = this._edgeMat;
      this._trimMesh.isPickable = false;
      markStaticMesh(this._trimMesh);
    }

    // The environment's shadow map is frozen (RENDER_ONCE) — re-bake it now
    // that the casters changed.
    if (this._env && this._env.refreshShadows) this._env.refreshShadows();
  }

  /** @private One merged mesh for every cluster union prism (sides + earcut
   *  roof cap via map-walls buildWallGeometry) and one for their unified
   *  trims. Bodies share the palette-tinted box material and cast shadows
   *  like the merged boxes; trims use the culling-disabled clone. */
  _buildClusterMeshes(unions, earcutFn) {
    if (!unions.length) return;
    const B = window.BABYLON;
    const body = { positions: [], normals: [], indices: [] };
    const trim = { positions: [], normals: [], indices: [] };
    for (const u of unions) {
      const wall = buildWallGeometry(u.groups, { height: PILLAR_HEIGHT, capWidth: 8, earcutFn });
      const base = body.positions.length / 3;
      for (const v of wall.positions) body.positions.push(v);
      for (const v of wall.normals) body.normals.push(v);
      for (const idx of wall.indices) body.indices.push(base + idx);
      const t = buildClusterTrimGeometry(u.groups, { height: PILLAR_HEIGHT, earcutFn });
      const tBase = trim.positions.length / 3;
      for (const v of t.positions) trim.positions.push(v);
      for (const v of t.normals) trim.normals.push(v);
      for (const idx of t.indices) trim.indices.push(tBase + idx);
    }

    const bodyMesh = new B.Mesh('obstacleClusterBodies', this.scene);
    const bodyData = new B.VertexData();
    bodyData.positions = body.positions;
    bodyData.normals = body.normals;
    bodyData.indices = body.indices;
    bodyData.applyToMesh(bodyMesh);
    bodyMesh.material = this._mat;
    bodyMesh.isPickable = false;
    markStaticMesh(bodyMesh);
    if (this._env && this._env.addShadowCaster) this._env.addShadowCaster(bodyMesh);
    this._clusterBodyMesh = bodyMesh;

    const trimMesh = new B.Mesh('obstacleClusterTrims', this.scene);
    const trimData = new B.VertexData();
    trimData.positions = trim.positions;
    trimData.normals = trim.normals;
    trimData.indices = trim.indices;
    trimData.applyToMesh(trimMesh);
    trimMesh.material = this._syncClusterTrimMat();
    trimMesh.isPickable = false;
    markStaticMesh(trimMesh);
    this._clusterTrimMesh = trimMesh;
  }

  /**
   * Force a rebuild of the current layout with fresh settings/palette state.
   * Called from the engine's settings-change hook when a world-identity
   * toggle flips mid-round; a no-op until the first keyframe delivers a
   * layout.
   */
  refresh() {
    if (!this._lastObstacles) return;
    this._lastFingerprint = null;
    this.update(this._lastObstacles, this._lastMaskRects);
  }

  /**
   * Every live round-built mesh (merged pillar bodies/trims + cluster
   * prisms + smooth boundary wall body/trim) for the intermission teardown
   * sink (issue #189). The director unfreezes and animates their
   * transforms; the next rebuild (or its fast-forward restore)
   * disposes/normalizes them. The boundary meshes are reached through the
   * owned MapWallsRenderer's private fields on purpose — this renderer
   * drives its whole lifecycle.
   * @returns {BABYLON.Mesh[]}
   */
  collectTeardownMeshes() {
    return [
      this._bodyMesh,
      this._trimMesh,
      this._clusterBodyMesh,
      this._clusterTrimMesh,
      this._mapWalls._bodyMesh,
      this._mapWalls._trimMesh,
    ].filter(Boolean);
  }

  /** Shared palette-tinted materials for the intermission construction
   *  clones, so the transient rise matches the final build exactly. */
  getConstructionMaterials() {
    return { body: this._mat, trim: this._edgeMat };
  }

  /** Last keyframe layout (teardown dust needs somewhere to burst). */
  getLastLayout() {
    return {
      obstacles: this._lastObstacles || [],
      maskRects: this._lastMaskRects || [],
    };
  }

  /** Clear all obstacles (round reset). */
  clear() {
    if (this._bodyMesh) { this._bodyMesh.dispose(); this._bodyMesh = null; }
    if (this._trimMesh) { this._trimMesh.dispose(); this._trimMesh = null; }
    if (this._clusterBodyMesh) { this._clusterBodyMesh.dispose(); this._clusterBodyMesh = null; }
    if (this._clusterTrimMesh) { this._clusterTrimMesh.dispose(); this._clusterTrimMesh = null; }
    this._mapWalls.clear();
  }
}
