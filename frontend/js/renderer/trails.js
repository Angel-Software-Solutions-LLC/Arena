'use strict';

/**
 * Movement trail system — bounded cosmetic ribbons and particle wakes.
 * Ribbons reuse one material, one mesh per visible bot, and fixed-size buffers.
 * Particle styles share one procedural texture and have a separate hard cap.
 * @module renderer/trails
 */

import { isEnabled } from '../settings.js';

const MAX_HISTORY = 24;
// Spectator positions arrive at 10 Hz. Sampling three times faster rebuilt the
// same ribbon geometry from interpolated points without adding useful truth.
const SAMPLE_INTERVAL = 0.1;
const MAX_RENDERED_TRAILS = 48;
// Queue selection already carries hysteresis to stay stable between frames, so
// rebuilding (and double-sorting) it at display rate only reproduced the same
// result with per-bot string allocations. Mirror the bots.js body-form LOD
// cadence; membership changes and reset() force an immediate rebuild.
const RENDER_QUEUE_REBUILD_INTERVAL_MS = 250;
const MAX_PARTICLE_SYSTEMS = 24;
const MAX_PARTICLES_PER_TRAIL = 28;
const TRAIL_SELECTION_HYSTERESIS_SQ = 40 * 40;
const TRAIL_WIDTH = 7.2;
const TRAIL_Y = 0.5;
// Paid ribbons draw a second narrow white-hot core over the wide glow layer.
const CORE_WIDTH_FRACTION = 0.3;
const CORE_WHITE_MIX = 0.6;

const STANDARD_STYLE = Object.freeze({
  key: 'standard',
  primary: '#63d8ff',
  secondary: '#b9f3ff',
  width: 0.72,
  alpha: 0.22,
  particles: null,
});

function particleStyle(emitRate, gravityY, options = {}) {
  return Object.freeze({
    emitRate,
    gravityY,
    minSize: options.minSize ?? 0.45,
    maxSize: options.maxSize ?? 1.25,
    minLife: options.minLife ?? 0.24,
    maxLife: options.maxLife ?? 0.72,
    spread: options.spread ?? 1.6,
    rise: options.rise ?? 0,
  });
}

function trailStyle(key, primary, secondary, width, alpha, particles, shape = {}) {
  // Paid trails must remain identifiable from spectator zoom, not merely tint
  // the free wake. Scale each authored signature while retaining its relative
  // width/opacity/emission character and the renderer's existing hard caps.
  // `shape` adds a per-style motion signature: `pulse` waves the ribbon's
  // edges, `jitter` staggers the samples into a zigzag arc.
  const emphasizedParticles = particles && Object.freeze({
    ...particles,
    emitRate: Math.max(26, particles.emitRate * 2.2),
    minSize: particles.minSize * 1.25,
    maxSize: particles.maxSize * 1.25,
  });
  return Object.freeze({
    key,
    primary,
    secondary,
    width: Math.max(1.35, width * 1.5),
    alpha: Math.max(0.62, alpha * 1.7),
    pulse: shape.pulse === true,
    jitter: shape.jitter === true,
    particles: emphasizedParticles,
  });
}

// Every server-provided asset key resolves through this fixed local allowlist.
// Styles are intentionally data-only so the shop preview and live arena use
// exactly the same presentation without loading remote scripts or textures.
const TRAIL_STYLES = Object.freeze({
  ember_sparks: trailStyle('ember_sparks', '#ff5b2e', '#ffd166', 0.86, 0.34, particleStyle(20, 2.8, {minLife: 0.18, maxLife: 0.52})),
  frost_shards: trailStyle('frost_shards', '#7be7ff', '#e5fbff', 0.92, 0.32, particleStyle(14, -1.8, {minSize: 0.35, maxSize: 0.95})),
  ion_stream: trailStyle('ion_stream', '#39f5ff', '#4b7cff', 0.72, 0.4, particleStyle(18, 0.4, {spread: 0.75, minLife: 0.3, maxLife: 0.82})),
  plasma_ribbon: trailStyle('plasma_ribbon', '#ff3fd1', '#7957ff', 1.12, 0.4, particleStyle(12, 0.8, {minSize: 0.7, maxSize: 1.5}), {pulse: true}),
  void_motes: trailStyle('void_motes', '#6d43c5', '#cc8cff', 1.02, 0.28, particleStyle(10, 1.6, {spread: 2.2, minLife: 0.45, maxLife: 1.05}), {pulse: true}),
  solar_wake: trailStyle('solar_wake', '#ff9e2c', '#fff4a8', 1.18, 0.4, particleStyle(19, 2.1, {minSize: 0.6, maxSize: 1.55}), {pulse: true}),
  lunar_dust: trailStyle('lunar_dust', '#aeb9da', '#ffffff', 0.96, 0.3, particleStyle(12, -0.7, {spread: 2.4, minLife: 0.5, maxLife: 1.1})),
  comet_tail: trailStyle('comet_tail', '#6ee7ff', '#ffffff', 1.3, 0.42, particleStyle(22, -0.35, {spread: 0.7, minLife: 0.24, maxLife: 0.68})),
  nebula_pulse: trailStyle('nebula_pulse', '#8f63ff', '#ff77cc', 1.18, 0.36, particleStyle(13, 0.9, {spread: 2.1, minSize: 0.75, maxSize: 1.65}), {pulse: true}),
  storm_arcs: trailStyle('storm_arcs', '#56b7ff', '#e8fbff', 0.84, 0.44, particleStyle(23, -2.4, {minSize: 0.25, maxSize: 0.78, minLife: 0.12, maxLife: 0.34}), {jitter: true}),
  static_glitch: trailStyle('static_glitch', '#00f0b5', '#f638dc', 0.76, 0.4, particleStyle(21, 0, {spread: 2.8, minSize: 0.25, maxSize: 0.82, minLife: 0.1, maxLife: 0.3}), {jitter: true}),
  pixel_scatter: trailStyle('pixel_scatter', '#57f287', '#78a7ff', 0.74, 0.34, particleStyle(17, -2.6, {minSize: 0.28, maxSize: 0.72, minLife: 0.28, maxLife: 0.7}), {jitter: true}),
  data_stream: trailStyle('data_stream', '#39ffb6', '#83f7ff', 0.68, 0.42, particleStyle(20, -1.2, {spread: 0.55, minSize: 0.22, maxSize: 0.58}), {jitter: true}),
  holo_prism: trailStyle('holo_prism', '#64e6ff', '#ff72d2', 1.02, 0.38, particleStyle(14, 0.5, {minSize: 0.6, maxSize: 1.35}), {pulse: true}),
  toxic_spores: trailStyle('toxic_spores', '#9bea37', '#e3ff75', 0.98, 0.34, particleStyle(15, 2.3, {spread: 2.7, minLife: 0.55, maxLife: 1.2}), {pulse: true}),
  verdant_leaves: trailStyle('verdant_leaves', '#35c96f', '#b8f26d', 0.9, 0.32, particleStyle(12, -3.2, {spread: 2.5, minSize: 0.45, maxSize: 1.05, minLife: 0.5, maxLife: 1.15})),
  sand_wake: trailStyle('sand_wake', '#c99a55', '#f0d58d', 1.14, 0.3, particleStyle(18, -3.6, {spread: 2.9, minLife: 0.38, maxLife: 0.95})),
  magma_cinders: trailStyle('magma_cinders', '#ff3d20', '#ffbf3f', 1.08, 0.4, particleStyle(19, 3.4, {minSize: 0.3, maxSize: 0.92, minLife: 0.35, maxLife: 0.88})),
  ocean_spray: trailStyle('ocean_spray', '#23aef3', '#b9fbff', 1.08, 0.36, particleStyle(18, -3, {spread: 2.25, minSize: 0.38, maxSize: 1.08}), {pulse: true}),
  gilded_dust: trailStyle('gilded_dust', '#dcae36', '#fff0a1', 0.92, 0.4, particleStyle(16, -0.8, {spread: 2.1, minLife: 0.48, maxLife: 1.05})),
  rune_sparks: trailStyle('rune_sparks', '#9a6cff', '#60e9ff', 0.96, 0.42, particleStyle(13, 1.8, {spread: 1.9, minSize: 0.5, maxSize: 1.1}), {jitter: true}),
  phantom_smoke: trailStyle('phantom_smoke', '#766b99', '#c8bce8', 1.24, 0.25, particleStyle(10, 2.5, {spread: 2.5, minSize: 0.9, maxSize: 1.85, minLife: 0.7, maxLife: 1.3}), {pulse: true}),
  gear_sparks: trailStyle('gear_sparks', '#d67b31', '#f7df92', 0.82, 0.38, particleStyle(20, -3.1, {minSize: 0.28, maxSize: 0.82, minLife: 0.2, maxLife: 0.55}), {jitter: true}),
  bounty_flare: trailStyle('bounty_flare', '#ffca3a', '#ff5a36', 1.16, 0.44, particleStyle(18, 2.7, {spread: 1.4, minSize: 0.6, maxSize: 1.4})),
});

/** Resolve an untrusted cosmetic key to one local, bounded style. */
export function resolveTrailStyle(assetKey) {
  if (typeof assetKey !== 'string') return STANDARD_STYLE;
  return TRAIL_STYLES[assetKey.trim().toLowerCase()] || STANDARD_STYLE;
}

function parseColor(B, value, fallback) {
  try {
    return B.Color3.FromHexString(value);
  } catch {
    return B.Color3.FromHexString(fallback);
  }
}

function cosmeticTrailKey(entry) {
  const raw = entry?.botData?.cosmetics?.trail;
  return typeof raw === 'string' && raw.trim() ? raw.trim().toLowerCase() : 'standard';
}

function pageHidden() {
  return typeof document !== 'undefined' && document.hidden === true;
}

function entryPosition(entry) {
  if (entry?.root?.parent && typeof entry.root.getAbsolutePosition === 'function') {
    return entry.root.getAbsolutePosition();
  }
  return entry?.root?.position;
}

export class TrailRenderer {
  /** @param {BABYLON.Scene} scene */
  constructor(scene, options = {}) {
    this.scene = scene;
    this.options = options;
    /** @type {Map<string, Object>} */
    this.trails = new Map();
    this.particleSystemCount = 0;
    this.sharedRibbonMaterial = null;
    this.sharedParticleTexture = null;
    this._styleColors = new Map();
    this._renderQueue = [];
    this._paidCandidates = [];
    this._standardCandidates = [];
    this._queueRefreshAt = 0;
    this._queueEntriesSize = -1;
    this._motionQuery = typeof window.matchMedia === 'function'
      ? window.matchMedia('(prefers-reduced-motion: reduce)')
      : null;
    this._time = 0;
  }

  _enabled() {
    return this.options.forceEnabled === true || isEnabled('movementTrails', 'botTrails');
  }

  _reducedMotion() {
    return this.options.reducedMotion === true || this._motionQuery?.matches === true;
  }

  _getSharedRibbonMaterial() {
    if (this.sharedRibbonMaterial) return this.sharedRibbonMaterial;
    const B = window.BABYLON;
    const material = new B.StandardMaterial('arena-cosmetic-trails-shared', this.scene);
    material.emissiveColor = new B.Color3(0.8, 0.8, 0.8);
    material.diffuseColor = new B.Color3(1, 1, 1);
    material.disableLighting = true;
    material.backFaceCulling = false;
    material.alpha = 1;
    material.useVertexAlpha = true;
    // Additive blending makes every wake read as light on the near-black
    // arena floor instead of a translucent decal.
    material.alphaMode = 1;
    material.freeze();
    this.sharedRibbonMaterial = material;
    return material;
  }

  _getSharedParticleTexture() {
    if (this.sharedParticleTexture) return this.sharedParticleTexture;
    const B = window.BABYLON;
    const texture = new B.DynamicTexture('arena-cosmetic-trail-particle', 16, this.scene, false);
    texture.hasAlpha = true;
    const context = texture.getContext();
    context.clearRect(0, 0, 16, 16);
    const gradient = context.createRadialGradient(8, 8, 0, 8, 8, 8);
    gradient.addColorStop(0, 'rgba(255,255,255,1)');
    gradient.addColorStop(0.38, 'rgba(255,255,255,0.9)');
    gradient.addColorStop(1, 'rgba(255,255,255,0)');
    context.fillStyle = gradient;
    context.fillRect(0, 0, 16, 16);
    texture.update(false);
    this.sharedParticleTexture = texture;
    return texture;
  }

  _getStyleColors(style) {
    let colors = this._styleColors.get(style.key);
    if (colors) return colors;
    const B = window.BABYLON;
    const primary = parseColor(B, style.primary, STANDARD_STYLE.primary);
    const secondary = parseColor(B, style.secondary, STANDARD_STYLE.secondary);
    colors = Object.freeze({
      primary,
      secondary,
      core: Object.freeze({
        r: secondary.r + (1 - secondary.r) * CORE_WHITE_MIX,
        g: secondary.g + (1 - secondary.g) * CORE_WHITE_MIX,
        b: secondary.b + (1 - secondary.b) * CORE_WHITE_MIX,
      }),
    });
    this._styleColors.set(style.key, colors);
    return colors;
  }

  _createParticleSystem(botId, entry, style) {
    if (!style.particles || this.particleSystemCount >= MAX_PARTICLE_SYSTEMS) return null;
    const B = window.BABYLON;
    const particles = new B.ParticleSystem(
      `cosmetic-trail-particles-${botId}`,
      MAX_PARTICLES_PER_TRAIL,
      this.scene,
    );
    particles.particleTexture = this._getSharedParticleTexture();
    particles.disposeOnStop = false;
    particles.emitter = entry.root;
    particles.minEmitBox = new B.Vector3(-0.8, 0.8, -1.6);
    particles.maxEmitBox = new B.Vector3(0.8, 2.8, 1.6);
    particles.direction1 = new B.Vector3(-style.particles.spread, style.particles.rise, -style.particles.spread);
    particles.direction2 = new B.Vector3(style.particles.spread, style.particles.rise, style.particles.spread);
    particles.gravity = new B.Vector3(0, style.particles.gravityY, 0);
    particles.minSize = style.particles.minSize;
    particles.maxSize = style.particles.maxSize;
    particles.minLifeTime = style.particles.minLife;
    particles.maxLifeTime = style.particles.maxLife;
    particles.emitRate = 0;
    particles.minAngularSpeed = -2.4;
    particles.maxAngularSpeed = 2.4;
    particles.blendMode = B.ParticleSystem.BLENDMODE_ADD;
    particles.updateSpeed = 0.012;
    this._applyParticleColors(particles, style);
    particles.start();
    this.particleSystemCount += 1;
    return particles;
  }

  _applyParticleColors(particles, style) {
    if (!particles) return;
    const B = window.BABYLON;
    const {primary, secondary} = this._getStyleColors(style);
    particles.color1 = new B.Color4(primary.r, primary.g, primary.b, 0.92);
    particles.color2 = new B.Color4(secondary.r, secondary.g, secondary.b, 0.78);
    particles.colorDead = new B.Color4(primary.r * 0.3, primary.g * 0.3, primary.b * 0.3, 0);
  }

  _disposeParticleSystem(trail) {
    if (!trail?.particles) return;
    trail.particles.stop();
    // The procedural texture belongs to this renderer and is shared by every
    // particle system, so per-bot cleanup must never dispose it.
    trail.particles.dispose(false);
    trail.particles = null;
    this.particleSystemCount = Math.max(0, this.particleSystemCount - 1);
  }

  _hideTrail(trail) {
    if (trail.mesh) trail.mesh.setEnabled(false);
    if (trail.coreMesh) trail.coreMesh.setEnabled(false);
    if (trail.particles) trail.particles.emitRate = 0;
  }

  /** Break every existing ribbon at the latest snapped bot position. */
  reset(botEntries) {
    // Resume-from-hidden must re-rank against fresh positions immediately, not
    // act on a queue captured up to 250ms before the tab was hidden.
    this._queueRefreshAt = 0;
    for (const [botId, trail] of this.trails) {
      const entry = botEntries?.get(botId);
      trail.timer = 0;
      trail.history.length = 0;
      if (entry?._interpReady) {
        const position = entryPosition(entry);
        if (position && this.options.previewPath === true) {
          trail.history.push({x: position.x - 15, z: position.z}, {x: position.x, z: position.z});
        } else if (position) {
          trail.history.push({x: position.x, z: position.z});
        }
      }
      trail.dirty = this.options.previewPath === true;
      trail.moving = this.options.staticPreview === true;
      this._hideTrail(trail);
    }
  }

  _createTrail(botId, entry, x, z, style) {
    const B = window.BABYLON;
    const left = [];
    const right = [];
    const coreLeft = [];
    const coreRight = [];
    for (let i = 0; i < MAX_HISTORY; i++) {
      left.push(new B.Vector3(x, TRAIL_Y, z));
      right.push(new B.Vector3(x, TRAIL_Y, z));
      coreLeft.push(new B.Vector3(x, TRAIL_Y, z));
      coreRight.push(new B.Vector3(x, TRAIL_Y, z));
    }
    const seededPreview = this.options.staticPreview === true || this.options.previewPath === true;
    const history = seededPreview
      ? [{x: x - 15, z}, {x, z}]
      : [{x, z}];
    return {
      history,
      mesh: null,
      coreMesh: null,
      particles: null,
      style,
      timer: 0,
      left,
      right,
      coreLeft,
      coreRight,
      colors: null,
      coreColors: null,
      dirty: seededPreview,
      moving: this.options.staticPreview === true,
      entry,
      botId,
    };
  }

  _updateStyle(trail, entry, style) {
    if (trail.style.key === style.key) return;
    this._disposeParticleSystem(trail);
    if (trail.coreMesh) {
      trail.coreMesh.dispose();
      trail.coreMesh = null;
      trail.coreColors = null;
    }
    trail.style = style;
    trail.entry = entry;
    trail.dirty = true;
  }

  /** @private A cached queue is valid only while every queued bot still exists. */
  _queueMembershipValid(botEntries) {
    if (botEntries.size !== this._queueEntriesSize) return false;
    for (const botId of this._renderQueue) {
      if (!botEntries.has(botId)) return false;
    }
    return true;
  }

  _buildRenderQueue(botEntries) {
    // Cadenced rebuild: selection hysteresis keeps membership stable between
    // frames, so re-ranking at display rate bought nothing. Any membership
    // change rebuilds immediately so the seen-set dispose loop below cannot
    // leave a departed bot's ribbon lingering for up to a cadence interval.
    const now = performance.now();
    if (now < this._queueRefreshAt && this._queueMembershipValid(botEntries)) {
      return this._renderQueue;
    }
    this._queueRefreshAt = now + RENDER_QUEUE_REBUILD_INTERVAL_MS;
    this._queueEntriesSize = botEntries.size;

    const queue = this._renderQueue;
    const paid = this._paidCandidates;
    const standard = this._standardCandidates;
    queue.length = 0;
    paid.length = 0;
    standard.length = 0;

    const cameraTarget = this.scene?.activeCamera?.target;
    const hasCameraTarget = Number.isFinite(cameraTarget?.x) && Number.isFinite(cameraTarget?.z);

    // Paid styles are an explicit visual entitlement. Rank them around the
    // current camera target before filling spare capacity with free wakes, so
    // neither free nor off-screen insertion order permanently hides a nearby
    // paid trail. A small existing-trail bonus prevents edge thrash.
    for (const [botId, entry] of botEntries) {
      if (!entry.isAlive || !entry._interpReady) continue;
      const position = entryPosition(entry);
      if (!position) continue;
      const dx = hasCameraTarget ? position.x - cameraTarget.x : 0;
      const dz = hasCameraTarget ? position.z - cameraTarget.z : 0;
      entry._trailPriorityScore = dx * dx + dz * dz
        - (this.trails.has(botId) ? TRAIL_SELECTION_HYSTERESIS_SQ : 0);
      if (resolveTrailStyle(cosmeticTrailKey(entry)).key === 'standard') standard.push(botId);
      else paid.push(botId);
    }

    const byCameraPriority = (leftId, rightId) => {
      const left = botEntries.get(leftId)?._trailPriorityScore ?? 0;
      const right = botEntries.get(rightId)?._trailPriorityScore ?? 0;
      return left - right;
    };
    paid.sort(byCameraPriority);
    standard.sort(byCameraPriority);

    for (let i = 0; i < paid.length && queue.length < MAX_RENDERED_TRAILS; i++) {
      queue.push(paid[i]);
    }
    if (this.options.showStandard !== false) {
      for (let i = 0; i < standard.length && queue.length < MAX_RENDERED_TRAILS; i++) {
        queue.push(standard[i]);
      }
    }
    return queue;
  }

  /**
   * Called every render frame with the bot renderer's entries map.
   * @param {Map<string, Object>|null} botEntries
   * @param {number} dt
   */
  render(botEntries, dt) {
    if (!botEntries) return;

    const suspended = pageHidden();
    const reducedMotion = this._reducedMotion();
    if (!this._enabled() || suspended) {
      for (const [, trail] of this.trails) this._hideTrail(trail);
      return;
    }

    const B = window.BABYLON;
    const seen = new Set();
    this._time += Number.isFinite(dt) ? Math.max(0, dt) : 0;

    for (const botId of this._buildRenderQueue(botEntries)) {
      const entry = botEntries.get(botId);
      seen.add(botId);

      // Per-frame cosmetic-change detection without per-frame string work:
      // the resolved style object is cached on the trail keyed by the RAW
      // server string, so an equipped-trail swap still lands on the very next
      // frame while the steady state performs zero trim/lowercase allocations.
      let trail = this.trails.get(botId);
      const rawStyleKey = entry?.botData?.cosmetics?.trail;
      const style = trail && trail._styleRaw === rawStyleKey
        ? trail.style
        : resolveTrailStyle(cosmeticTrailKey(entry));

      const position = entryPosition(entry);
      if (!position) continue;
      const x = position.x;
      const z = position.z;
      if (!trail) {
        trail = this._createTrail(botId, entry, x, z, style);
        trail._styleRaw = rawStyleKey;
        this.trails.set(botId, trail);
      } else if (trail._styleRaw !== rawStyleKey) {
        this._updateStyle(trail, entry, style);
        trail._styleRaw = rawStyleKey;
      }

      trail.timer += Number.isFinite(dt) ? Math.max(0, dt) : 0;
      if (this.options.staticPreview === true) {
        trail.moving = true;
      } else if (trail.timer >= SAMPLE_INTERVAL) {
        trail.timer %= SAMPLE_INTERVAL;
        const last = trail.history[trail.history.length - 1];
        const dx = x - last.x;
        const dz = z - last.z;
        const distanceSquared = dx * dx + dz * dz;
        if (distanceSquared > 150 * 150) {
          trail.history.length = 0;
          trail.history.push({x, z});
          trail.dirty = true;
          trail.moving = false;
        } else if (distanceSquared > 0.5) {
          trail.history.push({x, z});
          if (trail.history.length > MAX_HISTORY) trail.history.shift();
          trail.dirty = true;
          trail.moving = true;
        } else {
          trail.moving = false;
        }
      }

      if (style.particles && !reducedMotion && !trail.particles) {
        trail.particles = this._createParticleSystem(botId, entry, style);
      }
      if (trail.particles) {
        trail.particles.emitter = entry.root;
        trail.particles.emitRate = trail.moving && !reducedMotion
          ? style.particles.emitRate
          : 0;
      }

      // Animated shape signatures re-evaluate geometry while the bot moves.
      if ((style.pulse || style.jitter) && trail.moving && !reducedMotion) {
        trail.dirty = true;
      }

      if (trail.history.length < 2) {
        if (trail.mesh) trail.mesh.setEnabled(false);
        if (trail.coreMesh) trail.coreMesh.setEnabled(false);
        continue;
      }
      if (trail.mesh && !trail.mesh.isEnabled()) trail.mesh.setEnabled(true);
      if (trail.coreMesh && !trail.coreMesh.isEnabled()) trail.coreMesh.setEnabled(true);
      if (!trail.dirty && trail.mesh) continue;
      trail.dirty = false;

      const paid = style.key !== 'standard';
      const hist = trail.history;
      const n = hist.length;
      for (let i = 0; i < n; i++) {
        let nx;
        let nz;
        if (i < n - 1) {
          nx = hist[i + 1].x - hist[i].x;
          nz = hist[i + 1].z - hist[i].z;
        } else {
          nx = hist[i].x - hist[i - 1].x;
          nz = hist[i].z - hist[i - 1].z;
        }
        const len = Math.sqrt(nx * nx + nz * nz) || 1;
        const px = -nz / len;
        const pz = nx / len;
        const alpha = i / (n - 1);
        const wave = style.pulse ? 0.72 + 0.28 * Math.sin(i * 1.7 + this._time * 7) : 1;
        const zig = style.jitter ? (i % 2 ? 1 : -1) * TRAIL_WIDTH * style.width * 0.22 * alpha : 0;
        const width = TRAIL_WIDTH * style.width * alpha * wave;
        const cx = hist[i].x + px * zig;
        const cz = hist[i].z + pz * zig;
        trail.left[i].set(cx + px * width, TRAIL_Y, cz + pz * width);
        trail.right[i].set(cx - px * width, TRAIL_Y, cz - pz * width);
        if (paid) {
          const coreWidth = width * CORE_WIDTH_FRACTION;
          trail.coreLeft[i].set(cx + px * coreWidth, TRAIL_Y + 0.05, cz + pz * coreWidth);
          trail.coreRight[i].set(cx - px * coreWidth, TRAIL_Y + 0.05, cz - pz * coreWidth);
        }
      }
      for (let i = n; i < MAX_HISTORY; i++) {
        trail.left[i].copyFrom(trail.left[n - 1]);
        trail.right[i].copyFrom(trail.right[n - 1]);
        if (paid) {
          trail.coreLeft[i].copyFrom(trail.coreLeft[n - 1]);
          trail.coreRight[i].copyFrom(trail.coreRight[n - 1]);
        }
      }

      try {
        if (!trail.mesh) {
          const ribbon = B.MeshBuilder.CreateRibbon(`trail-${botId}`, {
            pathArray: [trail.left, trail.right],
            updatable: true,
            sideOrientation: B.Mesh.DOUBLESIDE,
          }, this.scene);
          ribbon.material = this._getSharedRibbonMaterial();
          ribbon.isPickable = false;
          ribbon.hasVertexAlpha = true;
          // The shared ribbon material is unlit (disableLighting with
          // vertex-color emissive output), so normals are never read.
          // Freezing them lets Babylon's ribbon instance-update path skip
          // ComputeNormals on every dirty frame.
          ribbon.freezeNormals();
          trail.mesh = ribbon;
          // Create the updatable ColorKind GPU buffer exactly once; dirty
          // frames then update it in place. (updateVerticesData silently
          // no-ops while the buffer does not exist, so this creation-time
          // setVerticesData is load-bearing.) Vertex count never changes:
          // paths are always padded to MAX_HISTORY.
          trail.colors = new Float32Array(ribbon.getTotalVertices() * 4);
          ribbon.setVerticesData(B.VertexBuffer.ColorKind, trail.colors, true);
          trail._colorSigN = -1;
        } else {
          B.MeshBuilder.CreateRibbon(null, {
            pathArray: [trail.left, trail.right],
            instance: trail.mesh,
          });
        }
        if (paid && !trail.coreMesh) {
          const core = B.MeshBuilder.CreateRibbon(`trail-core-${botId}`, {
            pathArray: [trail.coreLeft, trail.coreRight],
            updatable: true,
            sideOrientation: B.Mesh.DOUBLESIDE,
          }, this.scene);
          core.material = this._getSharedRibbonMaterial();
          core.isPickable = false;
          core.hasVertexAlpha = true;
          core.freezeNormals();
          trail.coreMesh = core;
          trail.coreColors = new Float32Array(core.getTotalVertices() * 4);
          core.setVerticesData(B.VertexBuffer.ColorKind, trail.coreColors, true);
          trail._colorSigN = -1;
        } else if (trail.coreMesh) {
          B.MeshBuilder.CreateRibbon(null, {
            pathArray: [trail.coreLeft, trail.coreRight],
            instance: trail.coreMesh,
          });
        }

        const bright = isEnabled('movementTrails', 'trailBrightness');
        let primary;
        let secondary;
        if (style.key === 'standard' && entry.bodyMat?.diffuseColor) {
          primary = entry.bodyMat.diffuseColor;
          secondary = entry.bodyMat.diffuseColor;
        } else {
          ({primary, secondary} = this._getStyleColors(style));
        }
        // A paid trail is an explicit visual entitlement: it never dims
        // behind the optional free-wake brightness toggle.
        const brightness = bright || paid || this.options.forceEnabled === true ? 1 : 0.55;
        // The vertex-color gradient depends only on this signature — not on
        // the pulse/jitter geometry animation that marks most dirty frames —
        // so the color loops and GPU upload run only when it changes. Color
        // VALUES are snapshotted because the standard style reads the live
        // avatar material reference, which can be recolored in place.
        const colorsCurrent = trail._colorSigN === n &&
          trail._colorSigKey === style.key &&
          trail._colorSigBright === brightness &&
          trail._colorSigPR === primary.r &&
          trail._colorSigPG === primary.g &&
          trail._colorSigPB === primary.b &&
          trail._colorSigSR === secondary.r &&
          trail._colorSigSG === secondary.g &&
          trail._colorSigSB === secondary.b;
        if (!colorsCurrent) {
          const vertexCount = trail.mesh.getTotalVertices();
          for (let vertex = 0; vertex < vertexCount; vertex++) {
            const index = vertex % MAX_HISTORY;
            const amount = index < n ? (index / (n - 1)) ** 0.8 : 0;
            const red = primary.r + (secondary.r - primary.r) * amount;
            const green = primary.g + (secondary.g - primary.g) * amount;
            const blue = primary.b + (secondary.b - primary.b) * amount;
            trail.colors[vertex * 4] = red * brightness;
            trail.colors[vertex * 4 + 1] = green * brightness;
            trail.colors[vertex * 4 + 2] = blue * brightness;
            trail.colors[vertex * 4 + 3] = index < n ? amount * style.alpha : 0;
          }
          trail.mesh.updateVerticesData(B.VertexBuffer.ColorKind, trail.colors);
          if (trail.coreMesh && trail.coreColors) {
            const core = this._getStyleColors(style).core;
            const coreCount = trail.coreMesh.getTotalVertices();
            for (let vertex = 0; vertex < coreCount; vertex++) {
              const index = vertex % MAX_HISTORY;
              const amount = index < n ? (index / (n - 1)) ** 0.8 : 0;
              trail.coreColors[vertex * 4] = core.r;
              trail.coreColors[vertex * 4 + 1] = core.g;
              trail.coreColors[vertex * 4 + 2] = core.b;
              trail.coreColors[vertex * 4 + 3] = index < n ? amount * 0.95 : 0;
            }
            trail.coreMesh.updateVerticesData(B.VertexBuffer.ColorKind, trail.coreColors);
          }
          trail._colorSigN = n;
          trail._colorSigKey = style.key;
          trail._colorSigBright = brightness;
          trail._colorSigPR = primary.r;
          trail._colorSigPG = primary.g;
          trail._colorSigPB = primary.b;
          trail._colorSigSR = secondary.r;
          trail._colorSigSG = secondary.g;
          trail._colorSigSB = secondary.b;
        }
      } catch {
        // A transient degenerate path is safe to skip; the next sample reuses
        // the same buffers and retries without allocating another mesh.
      }
    }

    for (const [botId, trail] of this.trails) {
      if (!seen.has(botId)) {
        if (trail.mesh) trail.mesh.dispose();
        if (trail.coreMesh) trail.coreMesh.dispose();
        this._disposeParticleSystem(trail);
        this.trails.delete(botId);
      }
    }
  }

  dispose() {
    for (const [, trail] of this.trails) {
      if (trail.mesh) trail.mesh.dispose();
      if (trail.coreMesh) trail.coreMesh.dispose();
      this._disposeParticleSystem(trail);
    }
    this.trails.clear();
    if (this.sharedRibbonMaterial) this.sharedRibbonMaterial.dispose();
    if (this.sharedParticleTexture) this.sharedParticleTexture.dispose();
    this.sharedRibbonMaterial = null;
    this.sharedParticleTexture = null;
    this._styleColors.clear();
    this._renderQueue.length = 0;
    this._paidCandidates.length = 0;
    this._standardCandidates.length = 0;
  }
}
