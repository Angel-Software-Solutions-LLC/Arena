'use strict';

/**
 * Arena environment — stone floor, boundary walls, dark void, safe zone ring.
 * @module renderer/environment
 */

import { makeMat } from './utils.js';
import { isEnabled } from '../settings.js';

const ZONE_RING_SEGMENTS = 64;
const ZONE_RING_BASE_ALPHA = 0.7;

/**
 * Per-map world identity (issue #182): one palette per map_shape, applied at
 * round build (zero per-frame cost). Every entry stays inside the arena's
 * editorial neon-on-navy language — hue leans, never full repaints. Fields:
 *  - obstacleBody: merged pillar material {diffuse, emissive} (linear 0-1)
 *  - obstacleTrim / wallTrim: emissive accents (linear 0-1)
 *  - swirl: energy-floor shader color uniform (linear 0-1)
 *  - floorBase: canvas radial-gradient center/mid RGB (0-255)
 *  - floorSpeckle: canvas speckle base RGB (0-255, r/g jittered as before)
 *  - floorPatch: canvas energy-patch gradient stops, two RGBs (0-255)
 */
const DEFAULT_MAP_PALETTE = Object.freeze({
  obstacleBody: { diffuse: [0.07, 0.085, 0.11], emissive: [0.015, 0.03, 0.05] },
  obstacleTrim: [0.08, 0.34, 0.62],
  wallTrim: [0.24, 0.72, 1.0],
  swirl: [0.04, 0.14, 0.34],
  floorBase: [[10, 20, 38], [6, 12, 22]],
  floorSpeckle: [80, 130, 255],
  floorPatch: [[40, 120, 255], [80, 180, 255]],
});

const MAP_PALETTES = Object.freeze({
  square: DEFAULT_MAP_PALETTE,
  circle: Object.freeze({ // ice cyan
    obstacleBody: { diffuse: [0.065, 0.09, 0.115], emissive: [0.012, 0.034, 0.052] },
    obstacleTrim: [0.06, 0.40, 0.58],
    wallTrim: [0.20, 0.80, 0.95],
    swirl: [0.03, 0.16, 0.33],
    floorBase: [[8, 22, 38], [5, 13, 22]],
    floorSpeckle: [70, 150, 255],
    floorPatch: [[30, 130, 255], [70, 190, 255]],
  }),
  hexagon: Object.freeze({ // gold-accented navy
    obstacleBody: { diffuse: [0.085, 0.082, 0.10], emissive: [0.032, 0.026, 0.04] },
    obstacleTrim: [0.52, 0.40, 0.10],
    wallTrim: [1.0, 0.81, 0.33],
    swirl: [0.10, 0.10, 0.24],
    floorBase: [[16, 17, 32], [9, 10, 20]],
    floorSpeckle: [200, 165, 110],
    floorPatch: [[120, 100, 220], [230, 190, 90]],
  }),
  diamond: Object.freeze({ // silvered white-blue
    obstacleBody: { diffuse: [0.08, 0.09, 0.115], emissive: [0.024, 0.03, 0.045] },
    obstacleTrim: [0.34, 0.44, 0.58],
    wallTrim: [0.72, 0.86, 1.0],
    swirl: [0.08, 0.13, 0.30],
    floorBase: [[13, 20, 34], [8, 12, 22]],
    floorSpeckle: [140, 165, 255],
    floorPatch: [[80, 130, 240], [150, 190, 255]],
  }),
  cross: Object.freeze({ // ember orange accents
    obstacleBody: { diffuse: [0.09, 0.08, 0.095], emissive: [0.036, 0.024, 0.03] },
    obstacleTrim: [0.55, 0.26, 0.08],
    wallTrim: [1.0, 0.54, 0.24],
    swirl: [0.13, 0.09, 0.22],
    floorBase: [[17, 15, 28], [10, 9, 18]],
    floorSpeckle: [220, 140, 90],
    floorPatch: [[150, 90, 200], [255, 150, 80]],
  }),
  caves: Object.freeze({ // warm amber/rust
    obstacleBody: { diffuse: [0.105, 0.085, 0.068], emissive: [0.046, 0.028, 0.014] },
    obstacleTrim: [0.58, 0.28, 0.08],
    wallTrim: [1.0, 0.62, 0.22],
    swirl: [0.15, 0.09, 0.11],
    floorBase: [[24, 17, 12], [13, 10, 8]],
    floorSpeckle: [235, 150, 70],
    floorPatch: [[200, 110, 50], [255, 170, 90]],
  }),
  donut: Object.freeze({ // magenta ring-world
    obstacleBody: { diffuse: [0.095, 0.07, 0.105], emissive: [0.038, 0.018, 0.044] },
    obstacleTrim: [0.48, 0.10, 0.44],
    wallTrim: [1.0, 0.36, 0.86],
    swirl: [0.15, 0.05, 0.26],
    floorBase: [[20, 10, 27], [12, 7, 17]],
    floorSpeckle: [225, 95, 255],
    floorPatch: [[180, 60, 230], [255, 120, 220]],
  }),
  islands: Object.freeze({ // aqua/teal
    obstacleBody: { diffuse: [0.06, 0.095, 0.10], emissive: [0.012, 0.038, 0.038] },
    obstacleTrim: [0.06, 0.42, 0.38],
    wallTrim: [0.24, 1.0, 0.82],
    swirl: [0.03, 0.18, 0.26],
    floorBase: [[7, 24, 28], [5, 14, 18]],
    floorSpeckle: [85, 230, 200],
    floorPatch: [[40, 190, 170], [90, 240, 210]],
  }),
  rooms: Object.freeze({ // slate/steel
    obstacleBody: { diffuse: [0.085, 0.095, 0.11], emissive: [0.028, 0.033, 0.042] },
    obstacleTrim: [0.24, 0.32, 0.42],
    wallTrim: [0.60, 0.72, 0.86],
    swirl: [0.07, 0.11, 0.21],
    floorBase: [[14, 18, 27], [9, 11, 18]],
    floorSpeckle: [135, 160, 210],
    floorPatch: [[70, 110, 190], [130, 165, 220]],
  }),
  spiral: Object.freeze({ // violet
    obstacleBody: { diffuse: [0.085, 0.075, 0.115], emissive: [0.032, 0.022, 0.052] },
    obstacleTrim: [0.30, 0.15, 0.56],
    wallTrim: [0.62, 0.42, 1.0],
    swirl: [0.11, 0.06, 0.30],
    floorBase: [[16, 12, 32], [10, 8, 20]],
    floorSpeckle: [165, 115, 255],
    floorPatch: [[120, 70, 230], [180, 130, 255]],
  }),
});


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

export class EnvironmentRenderer {
  /** @param {BABYLON.Scene} scene @param {number} w @param {number} h */
  constructor(scene, w, h) {
    this.scene = scene;
    this.w = w;
    this.h = h;
    this._zoneRing = null;
    this._targetRing = null;
    this._zoneMat = null;
    this._targetMat = null;
    this._lastZoneR = -1;
    this._lastZoneCx = -1;
    this._lastZoneCy = -1;
    this._zoneSuddenDeath = false;
    // Per-map identity state (issue #182): current shape + the obstacles of
    // the running round (for the baked floor contact shadows). Cluster
    // outlines (issue #190) shadow unified multi-rect structures as one
    // polygon instead of stacked per-rect fills.
    this._mapShape = 'square';
    this._roundObstacles = null;
    this._roundClusterOutlines = null;

    this._createSkybox();
    this._createSpaceObjects();
    this._createFloor();
    this._createLightShafts();
    this._createWalls();
    this._createCornerPylons();
    this._createPylonRings();
    this._createUndersideThrusters();
    this._createEdgeWaterfalls();
    this._createHoloTitle();
    this._initZoneMaterials();
    this._createAmbientParticles();
  }

  /** @private GPU-procedural space skybox — stars, nebulae, sun. All generated in the fragment shader. */
  _createSkybox() {
    const B = window.BABYLON;

    const skybox = B.MeshBuilder.CreateBox('skybox', { size: 50000 }, this.scene);
    skybox.infiniteDistance = true;
    skybox.renderingGroupId = 0;

    // Procedural space shader — generates stars and nebulae entirely on the GPU
    B.Effect.ShadersStore['spaceVertexShader'] = `
      precision highp float;
      attribute vec3 position;
      uniform mat4 worldViewProjection;
      varying vec3 vDir;
      void main() {
        gl_Position = worldViewProjection * vec4(position, 1.0);
        // Box position IS the direction — normalize for consistent star placement
        vDir = normalize(position);
      }
    `;

    B.Effect.ShadersStore['spaceFragmentShader'] = `
      precision highp float;
      varying vec3 vDir;
      uniform float time;

      // --- Hash functions for pseudo-random stars ---
      float hash(vec3 p) {
        p = fract(p * vec3(443.897, 441.423, 437.195));
        p += dot(p, p.yzx + 19.19);
        return fract((p.x + p.y) * p.z);
      }

      float hash2(vec2 p) {
        return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453123);
      }

      // --- Simplex-ish 3D noise for nebulae ---
      float noise3(vec3 p) {
        vec3 i = floor(p);
        vec3 f = fract(p);
        f = f * f * (3.0 - 2.0 * f);
        float n = dot(i, vec3(1.0, 57.0, 113.0));
        return mix(
          mix(mix(hash(i), hash(i + vec3(1,0,0)), f.x),
              mix(hash(i + vec3(0,1,0)), hash(i + vec3(1,1,0)), f.x), f.y),
          mix(mix(hash(i + vec3(0,0,1)), hash(i + vec3(1,0,1)), f.x),
              mix(hash(i + vec3(0,1,1)), hash(i + vec3(1,1,1)), f.x), f.y),
          f.z
        );
      }

      // Fractal Brownian Motion for nebula clouds
      // (4 octaves — the 5th cost a full noise3 per fragment for detail
      // that's invisible at sky distance behind the smoothstep threshold)
      float fbm(vec3 p) {
        float v = 0.0;
        float a = 0.5;
        for (int i = 0; i < 4; i++) {
          v += a * noise3(p);
          p *= 2.0;
          a *= 0.5;
        }
        return v;
      }

      // --- Star field ---
      float starField(vec3 dir, float scale, float threshold) {
        // Quantize direction to a grid on a sphere
        vec3 p = dir * scale;
        vec3 cell = floor(p);
        vec3 f = fract(p);

        float star = 0.0;
        // Check 3x3x3 neighborhood for nearest star
        for (int x = -1; x <= 1; x++) {
          for (int y = -1; y <= 1; y++) {
            for (int z = -1; z <= 1; z++) {
              vec3 neighbor = cell + vec3(float(x), float(y), float(z));
              // Random position within the cell
              vec3 starPos = neighbor + vec3(
                hash(neighbor),
                hash(neighbor + 100.0),
                hash(neighbor + 200.0)
              );
              float d = length(p - starPos);
              float brightness = hash(neighbor + 300.0);
              if (brightness > threshold) {
                float size = 0.015 + 0.025 * hash(neighbor + 400.0);
                // Twinkling
                float twinkle = 0.7 + 0.3 * sin(time * (1.0 + hash(neighbor + 500.0) * 3.0) + hash(neighbor + 600.0) * 6.28);
                float s = smoothstep(size, 0.0, d) * twinkle;
                // Glow for brighter stars
                if (brightness > 0.95) {
                  s += 0.3 * smoothstep(size * 6.0, 0.0, d) * twinkle;
                }
                star = max(star, s);
              }
            }
          }
        }
        return star;
      }

      void main() {
        vec3 dir = normalize(vDir);

        // Base color: very dark blue-black
        vec3 color = vec3(0.002, 0.003, 0.012);

        // --- Nebula layers ---
        // Purple/blue nebula band
        float n1 = fbm(dir * 3.0 + vec3(42.0, 0.0, 17.0));
        float n2 = fbm(dir * 2.0 + vec3(0.0, 33.0, 0.0));
        float nebula1 = smoothstep(0.4, 0.7, n1) * 0.15;
        float nebula2 = smoothstep(0.45, 0.75, n2) * 0.1;
        color += vec3(0.08, 0.02, 0.15) * nebula1; // purple
        color += vec3(0.02, 0.05, 0.12) * nebula2; // blue

        // Warm nebula accent
        float n3 = fbm(dir * 4.0 + vec3(100.0, 50.0, 75.0));
        float nebula3 = smoothstep(0.5, 0.8, n3) * 0.08;
        color += vec3(0.12, 0.04, 0.02) * nebula3; // warm red-orange

        // --- Stars at two scales ---
        // (each starField call walks a 3x3x3 cell neighborhood, so the
        // mid 40.0 layer was dropped — dense + sparse covers the range and
        // it's a third of the heaviest loop back for integrated GPUs)
        // Dense small stars
        float s1 = starField(dir, 80.0, 0.7);
        // Sparse bright stars
        float s3 = starField(dir, 20.0, 0.9);

        // Star color temperature varies
        vec3 starColor1 = mix(vec3(0.7, 0.8, 1.0), vec3(1.0, 0.95, 0.8), hash(dir * 80.0 + 999.0));
        vec3 starColor3 = mix(vec3(0.9, 0.95, 1.0), vec3(1.0, 0.85, 0.6), hash(dir * 20.0 + 777.0));

        color += starColor1 * s1 * 0.6;
        color += starColor3 * s3 * 1.2;

        // --- Distant sun glow (optional, matching the reference) ---
        vec3 sunDir = normalize(vec3(0.5, 0.3, -0.8));
        float sunDot = max(dot(dir, sunDir), 0.0);
        color += vec3(0.15, 0.08, 0.02) * pow(sunDot, 32.0); // tight glow
        color += vec3(0.05, 0.03, 0.01) * pow(sunDot, 4.0);  // wide haze

        gl_FragColor = vec4(color, 1.0);
      }
    `;

    const skyMat = new B.ShaderMaterial('skyboxMat', this.scene, {
      vertex: 'space',
      fragment: 'space'
    }, {
      attributes: ['position'],
      uniforms: ['worldViewProjection', 'time']
    });
    skyMat.setFloat('time', 0);
    skyMat.backFaceCulling = false;

    skybox.material = skyMat;
    skybox.isPickable = false;
    // The custom shader has no fog branch, so the sky can never be fogged by
    // the depth-fog setting; applyFog=false documents that contract.
    skybox.applyFog = false;

    // Animate twinkling + live settings check (setEnabled is cheap enough
    // to call every frame; avoids needing a separate settings-change hook).
    this.scene.registerBeforeRender(() => {
      const on = isEnabled('arenaAmbience', 'skybox');
      skybox.setEnabled(on);
      if (on) skyMat.setFloat('time', performance.now() / 1000);
    });

    this._skybox = skybox;
  }

  /** @private Space objects flying around — satellites, comets, UFOs, Elon's Tesla. */
  _createSpaceObjects() {
    const B = window.BABYLON;
    const scene = this.scene;
    const cx = this.w / 2;
    const cz = this.h / 2;
    const minDist = Math.max(this.w, this.h) * 1.5; // never closer than 1.5x arena
    const maxDist = Math.max(this.w, this.h) * 3.5;

    // --- Sprite generation on canvas ---
    const makeTexture = (name, drawFn, size = 64) => {
      const c = document.createElement('canvas');
      c.width = size; c.height = size;
      const ctx = c.getContext('2d');
      drawFn(ctx, size);
      const tex = new B.DynamicTexture(name, { width: size, height: size }, scene, false);
      tex.getContext().drawImage(c, 0, 0);
      tex.update();
      tex.hasAlpha = true;
      return tex;
    };

    // 🛰️ Satellite
    const satelliteTex = makeTexture('satTex', (ctx, s) => {
      ctx.clearRect(0, 0, s, s);
      // Body
      ctx.fillStyle = '#888';
      ctx.fillRect(s*0.35, s*0.3, s*0.3, s*0.4);
      // Solar panels
      ctx.fillStyle = '#2266cc';
      ctx.fillRect(s*0.05, s*0.38, s*0.28, s*0.24);
      ctx.fillRect(s*0.67, s*0.38, s*0.28, s*0.24);
      // Panel grid lines
      ctx.strokeStyle = '#1144aa';
      ctx.lineWidth = 1;
      for (let i = 0; i < 3; i++) {
        ctx.beginPath();
        ctx.moveTo(s*0.05 + s*0.07*i + s*0.035, s*0.38);
        ctx.lineTo(s*0.05 + s*0.07*i + s*0.035, s*0.62);
        ctx.stroke();
        ctx.beginPath();
        ctx.moveTo(s*0.67 + s*0.07*i + s*0.035, s*0.38);
        ctx.lineTo(s*0.67 + s*0.07*i + s*0.035, s*0.62);
        ctx.stroke();
      }
      // Antenna
      ctx.strokeStyle = '#aaa';
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(s*0.5, s*0.3);
      ctx.lineTo(s*0.5, s*0.12);
      ctx.stroke();
      // Dish
      ctx.beginPath();
      ctx.arc(s*0.5, s*0.12, s*0.06, 0, Math.PI*2);
      ctx.fillStyle = '#ccc';
      ctx.fill();
    });

    // ☄️ Comet — just the glowing core (particle trail IS the tail)
    const cometTex = makeTexture('cometTex', (ctx, s) => {
      ctx.clearRect(0, 0, s, s);
      // Bright core glow
      const coreGrad = ctx.createRadialGradient(s*0.5, s*0.5, 0, s*0.5, s*0.5, s*0.4);
      coreGrad.addColorStop(0, 'rgba(255,255,255,1)');
      coreGrad.addColorStop(0.2, 'rgba(220,240,255,0.9)');
      coreGrad.addColorStop(0.5, 'rgba(150,200,255,0.5)');
      coreGrad.addColorStop(1, 'rgba(80,140,255,0)');
      ctx.fillStyle = coreGrad;
      ctx.fillRect(0, 0, s, s);
    });

    // Small white circle for comet-trail particles, shared across all comet
    // respawns instead of rebuilt per trail; freed once in dispose() like
    // _pylonGlowTex (ParticleSystem.dispose() defaults to tearing down its
    // texture, so trails must always dispose with `false`).
    const trailTex = new B.DynamicTexture('cometTrailTex', 16, scene, false);
    const ptCtx = trailTex.getContext();
    const ptGrad = ptCtx.createRadialGradient(8, 8, 0, 8, 8, 8);
    ptGrad.addColorStop(0, 'rgba(255,255,255,1)');
    ptGrad.addColorStop(1, 'rgba(255,255,255,0)');
    ptCtx.fillStyle = ptGrad;
    ptCtx.fillRect(0, 0, 16, 16);
    trailTex.update();
    this._cometTrailTex = trailTex;

    // 🚗 Tesla Roadster (Starman!)
    const teslaTex = makeTexture('teslaTex', (ctx, s) => {
      ctx.clearRect(0, 0, s, s);
      // Car body — red
      ctx.fillStyle = '#cc2222';
      ctx.beginPath();
      ctx.ellipse(s*0.5, s*0.55, s*0.32, s*0.12, 0, 0, Math.PI*2);
      ctx.fill();
      // Roof
      ctx.fillStyle = '#aa1818';
      ctx.beginPath();
      ctx.ellipse(s*0.45, s*0.48, s*0.18, s*0.08, -0.1, 0, Math.PI*2);
      ctx.fill();
      // Windshield
      ctx.fillStyle = 'rgba(100,180,255,0.5)';
      ctx.beginPath();
      ctx.ellipse(s*0.55, s*0.47, s*0.08, s*0.06, 0.2, 0, Math.PI*2);
      ctx.fill();
      // Wheels
      ctx.fillStyle = '#333';
      ctx.beginPath();
      ctx.arc(s*0.3, s*0.63, s*0.06, 0, Math.PI*2);
      ctx.fill();
      ctx.beginPath();
      ctx.arc(s*0.65, s*0.63, s*0.06, 0, Math.PI*2);
      ctx.fill();
      // Starman (astronaut silhouette in driver seat)
      ctx.fillStyle = '#eee';
      ctx.beginPath();
      ctx.arc(s*0.48, s*0.4, s*0.04, 0, Math.PI*2); // helmet
      ctx.fill();
      ctx.fillStyle = '#ddd';
      ctx.fillRect(s*0.46, s*0.44, s*0.04, s*0.06); // body
      // "DON'T PANIC" on dashboard (too small but implied)
    }, 128);

    // 👽 UFO
    const ufoTex = makeTexture('ufoTex', (ctx, s) => {
      ctx.clearRect(0, 0, s, s);
      // Dome
      ctx.fillStyle = 'rgba(150,200,255,0.5)';
      ctx.beginPath();
      ctx.ellipse(s*0.5, s*0.4, s*0.12, s*0.15, 0, Math.PI, 0);
      ctx.fill();
      // Saucer body
      ctx.fillStyle = '#777';
      ctx.beginPath();
      ctx.ellipse(s*0.5, s*0.45, s*0.35, s*0.1, 0, 0, Math.PI*2);
      ctx.fill();
      // Lights along the rim
      const colors = ['#ff4444', '#44ff44', '#4444ff', '#ffff44', '#ff44ff'];
      for (let i = 0; i < 5; i++) {
        const angle = (i / 5) * Math.PI * 2 - Math.PI/2;
        const lx = s*0.5 + Math.cos(angle) * s*0.28;
        const ly = s*0.45 + Math.sin(angle) * s*0.07;
        ctx.beginPath();
        ctx.arc(lx, ly, s*0.02, 0, Math.PI*2);
        ctx.fillStyle = colors[i];
        ctx.fill();
      }
      // Beam (subtle)
      const beam = ctx.createLinearGradient(s*0.4, s*0.5, s*0.4, s*0.9);
      beam.addColorStop(0, 'rgba(100,255,100,0.15)');
      beam.addColorStop(1, 'rgba(100,255,100,0)');
      ctx.fillStyle = beam;
      ctx.beginPath();
      ctx.moveTo(s*0.35, s*0.52);
      ctx.lineTo(s*0.25, s*0.9);
      ctx.lineTo(s*0.75, s*0.9);
      ctx.lineTo(s*0.65, s*0.52);
      ctx.fill();
    });

    // 🛸 Space Debris (ISS-like)
    const debrisTex = makeTexture('debrisTex', (ctx, s) => {
      ctx.clearRect(0, 0, s, s);
      ctx.fillStyle = '#999';
      ctx.fillRect(s*0.2, s*0.45, s*0.6, s*0.1); // truss
      ctx.fillStyle = '#336699';
      ctx.fillRect(s*0.1, s*0.35, s*0.15, s*0.3); // panel L
      ctx.fillRect(s*0.75, s*0.35, s*0.15, s*0.3); // panel R
      ctx.fillStyle = '#aaa';
      ctx.fillRect(s*0.4, s*0.4, s*0.2, s*0.2); // module
    });

    const objectDefs = [
      { name: 'satellite', tex: satelliteTex, size: 250, speed: [15, 35], weight: 3 },
      { name: 'comet',     tex: cometTex,     size: 400, speed: [60, 120], weight: 2 },
      { name: 'tesla',     tex: teslaTex,     size: 350, speed: [20, 40],  weight: 1 },
      { name: 'ufo',       tex: ufoTex,       size: 300, speed: [25, 55],  weight: 2 },
      { name: 'debris',    tex: debrisTex,     size: 200, speed: [10, 25], weight: 2 },
    ];

    // Build weighted pool
    const pool = [];
    for (const def of objectDefs) {
      for (let i = 0; i < def.weight; i++) pool.push(def);
    }

    // Active space objects
    this._spaceObjects = [];
    const MAX_OBJECTS = 6;

    const randomPointOnShell = () => {
      // Random point on a sphere shell between minDist and maxDist from arena center
      const dist = minDist + Math.random() * (maxDist - minDist);
      const theta = Math.random() * Math.PI * 2;
      const phi = Math.acos(2 * Math.random() - 1); // uniform sphere distribution
      return new B.Vector3(
        cx + dist * Math.sin(phi) * Math.cos(theta),
        dist * Math.sin(phi) * Math.sin(theta) * 0.6, // slightly flatten vertical
        cz + dist * Math.cos(phi)
      );
    };

    const spawnObject = (reuseMat = null) => {
      const def = pool[Math.floor(Math.random() * pool.length)];

      // Start position — random point on the outer shell
      const start = randomPointOnShell();

      // End position — opposite-ish side of the arena
      const end = randomPointOnShell();

      // Create billboard sprite
      const plane = B.MeshBuilder.CreatePlane('space_' + def.name + '_' + Date.now(), {
        width: def.size,
        height: def.size
      }, scene);

      // Reuse the slot's material on respawn — only the sprite textures
      // change between object types; the static flags are set once.
      const mat = reuseMat || new B.StandardMaterial('spMat_' + Date.now(), scene);
      mat.diffuseTexture = def.tex;
      mat.emissiveTexture = def.tex; // self-lit in space
      mat.opacityTexture = def.tex;
      if (!reuseMat) {
        mat.emissiveColor = new B.Color3(0.8, 0.8, 0.8);
        mat.backFaceCulling = false;
        mat.disableLighting = true;
      }
      plane.material = mat;
      plane.billboardMode = B.Mesh.BILLBOARDMODE_ALL;
      plane.position = start.clone();
      plane.isPickable = false;
      // Space objects live 1.5-3.5 arena-widths out — the depth fog tuned
      // for the arena floor would erase them entirely at that distance.
      plane.applyFog = false;

      // Add a comet particle trail
      let trail = null;
      if (def.name === 'comet') {
        trail = new B.ParticleSystem('cometTrail_' + Date.now(), 50, scene);
        trail.emitter = plane;
        trail.minSize = 15;
        trail.maxSize = 40;
        trail.minLifeTime = 1.0;
        trail.maxLifeTime = 2.5;
        trail.emitRate = 60;
        trail.color1 = new B.Color4(0.5, 0.7, 1.0, 0.6);
        trail.color2 = new B.Color4(0.3, 0.5, 0.8, 0.3);
        trail.colorDead = new B.Color4(0.1, 0.2, 0.5, 0);
        trail.direction1 = new B.Vector3(-1, -1, -1);
        trail.direction2 = new B.Vector3(1, 1, 1);
        trail.minEmitPower = 1;
        trail.maxEmitPower = 3;
        trail.gravity = new B.Vector3(0, 0, 0);
        trail.blendMode = B.ParticleSystem.BLENDMODE_ADD;
        trail.particleTexture = this._cometTrailTex; // shared — see hoist above
        trail.start();
      }

      // UFO gets a slight wobble
      const wobble = def.name === 'ufo' ? 0.3 + Math.random() * 0.5 : 0;

      const speed = def.speed[0] + Math.random() * (def.speed[1] - def.speed[0]);
      const dir = end.subtract(start).normalize();
      const totalDist = B.Vector3.Distance(start, end);
      const duration = totalDist / speed;

      return { plane, mat, trail, start, dir, speed, duration, elapsed: 0, wobble, def };
    };

    // Spawn initial batch (staggered)
    for (let i = 0; i < MAX_OBJECTS; i++) {
      const obj = spawnObject();
      obj.elapsed = Math.random() * obj.duration; // random progress so they don't all start at once
      obj.plane.position = obj.start.add(obj.dir.scale(obj.speed * obj.elapsed));
      this._spaceObjects.push(obj);
    }

    const COMET_TRAIL_EMIT_RATE = 60;

    // Animation loop
    scene.registerBeforeRender(() => {
      const on = isEnabled('arenaAmbience', 'spaceObjects');
      if (!on) {
        // Hide and freeze in place rather than disposing/skipping elapsed
        // advance — keeps respawn timing sane for when it's re-enabled.
        for (const obj of this._spaceObjects) {
          obj.plane.setEnabled(false);
          if (obj.trail) obj.trail.emitRate = 0;
        }
        return;
      }

      const dt = scene.getEngine().getDeltaTime() / 1000;
      for (let i = 0; i < this._spaceObjects.length; i++) {
        const obj = this._spaceObjects[i];
        obj.plane.setEnabled(true);
        if (obj.trail) obj.trail.emitRate = COMET_TRAIL_EMIT_RATE;
        obj.elapsed += dt;
        // Mutate the existing position vector in place — .add()/.scale()
        // here allocated 2 fresh Vector3 per object per frame.
        obj.plane.position.copyFrom(obj.start);
        obj.dir.scaleAndAddToRef(obj.speed * obj.elapsed, obj.plane.position);

        // UFO wobble
        if (obj.wobble > 0) {
          obj.plane.position.y += Math.sin(obj.elapsed * 3) * obj.wobble * 20;
        }

        // Rotate satellites and debris slowly
        if (obj.def.name === 'satellite' || obj.def.name === 'debris') {
          obj.plane.rotation.z = obj.elapsed * 0.3;
        }

        // Tesla tumbles slowly (like the real one!)
        if (obj.def.name === 'tesla') {
          obj.plane.rotation.z = obj.elapsed * 0.15;
        }



        // Respawn when it's traveled its full path
        if (obj.elapsed >= obj.duration) {
          // Cleanup — the material is handed back to spawnObject() for
          // reuse, and the trail texture is the shared _cometTrailTex
          // (hence dispose(false); see the hoist above).
          obj.plane.dispose();
          if (obj.trail) { obj.trail.dispose(false); }

          // Replace with new object
          this._spaceObjects[i] = spawnObject(obj.mat);
        }
      }
    });
  }

  /** Current effective palette — the map's own when the toggle is on. */
  getPalette() {
    return this.getPaletteForShape(this._mapShape);
  }

  /** Effective palette for an arbitrary shape, honoring the mapPalettes
   *  toggle. The intermission director uses this to tint the NEXT map's
   *  construction clones before setMapShape flips the stage identity
   *  (issue #192 color parity — must resolve exactly like getPalette). */
  getPaletteForShape(shape) {
    if (!isEnabled('arenaAmbience', 'mapPalettes')) return DEFAULT_MAP_PALETTE;
    return MAP_PALETTES[shape] || DEFAULT_MAP_PALETTE;
  }

  /**
   * Feed the round's map shape from the spectator state. Only a real change
   * re-themes (round boundary); non-keyframe states without the field no-op.
   */
  setMapShape(shape) {
    if (typeof shape !== 'string' || !shape || shape === this._mapShape) return;
    this._mapShape = shape;
    this.applyMapTheme();
  }

  /**
   * Round-build hook from ObstacleRenderer: remember the layout and re-bake
   * the floor so the contact shadows sit under this round's obstacles.
   * @param {Array} obstacles isolated rects, shadowed as rects
   * @param {Array} [clusterOutlines] union outline groups
   *   ({outer:[[x,z]...],holes:[...]}) for multi-rect clusters (issue #190),
   *   each shadowed as one polygon along its exact outline
   */
  setRoundObstacles(obstacles, clusterOutlines) {
    this._roundObstacles = Array.isArray(obstacles) ? obstacles : null;
    this._roundClusterOutlines =
      Array.isArray(clusterOutlines) && clusterOutlines.length ? clusterOutlines : null;
    this._paintFloor();
  }

  /**
   * (Re)apply the current palette to everything environment-owned: wall trim
   * emissive, energy-swirl shader uniform, and the baked floor canvas. Called
   * on map-shape changes and from the engine's settings-change hook — never
   * per frame.
   */
  applyMapTheme() {
    const palette = this.getPalette();
    if (this._wallTrimMat) {
      this._wallTrimMat.unfreeze();
      this._wallTrimMat.emissiveColor.set(...palette.wallTrim);
      this._wallTrimMat.freeze();
    }
    if (this._floorGlowMat) {
      const B = window.BABYLON;
      this._floorGlowMat.setColor3('swirlColor', new B.Color3(...palette.swirl));
    }
    this._paintFloor();
  }

  /**
   * @private Bake the floor deck canvas: palette-tinted gradient + speckles +
   * energy patches (the pre-#182 look, hues parametrized), then soft-edged
   * darkened rects under each obstacle footprint — cheap baked contact
   * shadows that ground the merged pillars. Runs at round build only.
   */
  _paintFloor() {
    if (!this._floorCanvas || !this._floorTex) return;
    const ctx = this._floorCanvas.getContext('2d');
    const palette = this.getPalette();
    ctx.clearRect(0, 0, 1024, 1024);

    const [baseCenter, baseMid] = palette.floorBase;
    const grad = ctx.createRadialGradient(512, 512, 90, 512, 512, 640);
    grad.addColorStop(0, `rgba(${baseCenter[0]},${baseCenter[1]},${baseCenter[2]},0.35)`);
    grad.addColorStop(0.55, `rgba(${baseMid[0]},${baseMid[1]},${baseMid[2]},0.2)`);
    grad.addColorStop(1, 'rgba(2,4,8,0.05)');
    ctx.fillStyle = grad;
    ctx.fillRect(0, 0, 1024, 1024);

    const sp = palette.floorSpeckle;
    for (let i = 0; i < 2600; i++) {
      const x = Math.random() * 1024;
      const y = Math.random() * 1024;
      const r = 0.6 + Math.random() * 2.2;
      const a = 0.018 + Math.random() * 0.05;
      ctx.beginPath();
      ctx.arc(x, y, r, 0, Math.PI * 2);
      ctx.fillStyle = `rgba(${Math.min(255, sp[0] + (Math.random() * 50 | 0))},${Math.min(255, sp[1] + (Math.random() * 60 | 0))},${sp[2]},${a.toFixed(3)})`;
      ctx.fill();
    }

    const [patchA, patchB] = palette.floorPatch;
    for (let i = 0; i < 26; i++) {
      const x = Math.random() * 1024;
      const y = Math.random() * 1024;
      const w = 80 + Math.random() * 180;
      const h = 40 + Math.random() * 110;
      const g = ctx.createRadialGradient(x, y, 0, x, y, Math.max(w, h));
      g.addColorStop(0, `rgba(${patchA[0]},${patchA[1]},${patchA[2]},0.08)`);
      g.addColorStop(0.45, `rgba(${patchB[0]},${patchB[1]},${patchB[2]},0.03)`);
      g.addColorStop(1, 'rgba(0,0,0,0)');
      ctx.fillStyle = g;
      ctx.fillRect(x - w, y - h, w * 2, h * 2);
    }

    // Contact shadows (issue #182b): the ground UVs run u with +x and v with
    // +z, and DynamicTexture uploads invert Y, so canvas y = (1 - z/h)*1024.
    // Three nested fills approximate an ~8px feathered edge without relying
    // on ctx.filter (not universal); cumulative center darkening ~35%.
    if ((this._roundObstacles || this._roundClusterOutlines) &&
        isEnabled('arenaAmbience', 'contactShadows')) {
      const pxX = 1024 / this.w;
      const pxY = 1024 / this.h;
      const feather = 8;
      for (const obs of this._roundObstacles || []) {
        const cx = obs.x * pxX;
        const cy = (1 - (obs.y + obs.height) / this.h) * 1024;
        const cw = obs.width * pxX;
        const ch = obs.height * pxY;
        ctx.fillStyle = 'rgba(0,0,6,0.13)';
        ctx.fillRect(cx - feather, cy - feather, cw + feather * 2, ch + feather * 2);
        ctx.fillRect(cx - feather / 2, cy - feather / 2, cw + feather, ch + feather);
        ctx.fillStyle = 'rgba(0,0,6,0.14)';
        ctx.fillRect(cx, cy, cw, ch);
      }
      // Cluster unions (issue #190): one polygon-shaped shadow per union,
      // traced along the exact outline (holes carved via even-odd), with
      // the feather approximated by two centered strokes — stacked per-rect
      // fills used to double-darken wherever member rects abutted.
      if (this._roundClusterOutlines) {
        ctx.lineJoin = 'miter';
        for (const group of this._roundClusterOutlines) {
          ctx.beginPath();
          for (const ring of [group.outer, ...(group.holes || [])]) {
            if (!ring || ring.length < 3) continue;
            ctx.moveTo(ring[0][0] * pxX, (1 - ring[0][1] / this.h) * 1024);
            for (let i = 1; i < ring.length; i++) {
              ctx.lineTo(ring[i][0] * pxX, (1 - ring[i][1] / this.h) * 1024);
            }
            ctx.closePath();
          }
          ctx.fillStyle = 'rgba(0,0,6,0.13)';
          ctx.strokeStyle = 'rgba(0,0,6,0.13)';
          ctx.lineWidth = feather * 2;
          ctx.fill('evenodd');
          ctx.stroke();
          ctx.lineWidth = feather;
          ctx.fill('evenodd');
          ctx.stroke();
          ctx.fillStyle = 'rgba(0,0,6,0.14)';
          ctx.fill('evenodd');
        }
      }
    }

    this._floorTex.getContext().drawImage(this._floorCanvas, 0, 0);
    this._floorTex.update();
  }

  /** @private Transparent arena floor with soft ambient energy motion. */
  _createFloor() {
    const B = window.BABYLON;
    const ground = B.MeshBuilder.CreateGround('ground', {
      width: this.w, height: this.h, subdivisions: 4
    }, this.scene);
    ground.position.x = this.w / 2;
    ground.position.z = this.h / 2;

    this._floorCanvas = document.createElement('canvas');
    this._floorCanvas.width = 1024;
    this._floorCanvas.height = 1024;
    this._floorTex = new B.DynamicTexture('floorDeckTex', { width: 1024, height: 1024 }, this.scene, false);
    this._paintFloor();
    const floorTex = this._floorTex;

    const mat = new B.StandardMaterial('floorMat', this.scene);
    mat.diffuseTexture = floorTex;
    mat.emissiveTexture = floorTex;
    mat.diffuseColor = new B.Color3(0.12, 0.18, 0.28);
    mat.emissiveColor = new B.Color3(0.08, 0.14, 0.24);
    mat.specularColor = new B.Color3(0.05, 0.07, 0.1);
    mat.alpha = 0.34;
    mat.backFaceCulling = false;

    ground.material = mat;
    ground.isPickable = false;
    ground.receiveShadows = true;
    markStaticMesh(ground);

    // Add a second layer — very soft ambient energy motion with no grid structure.
    const glow = B.MeshBuilder.CreateGround('floorGlow', {
      width: this.w, height: this.h, subdivisions: 2
    }, this.scene);
    glow.position.set(this.w / 2, 0.24, this.h / 2);

    B.Effect.ShadersStore['energyFloorVertexShader'] = `
      precision highp float;
      attribute vec3 position;
      attribute vec2 uv;
      uniform mat4 worldViewProjection;
      varying vec2 vUV;
      void main() {
        gl_Position = worldViewProjection * vec4(position, 1.0);
        vUV = uv;
      }
    `;
    B.Effect.ShadersStore['energyFloorFragmentShader'] = `
      precision highp float;
      varying vec2 vUV;
      uniform float time;
      uniform vec3 swirlColor;
      void main() {
        vec2 p = vUV - 0.5;
        float dist = length(p);
        float swirl = 0.5 + 0.5 * sin((dist * 16.0 - time * 0.8) + sin(vUV.x * 5.5 + time * 0.16) * 1.5);
        float cloud = 0.5 + 0.5 * sin(vUV.x * 9.0 + time * 0.1) * sin(vUV.y * 7.0 - time * 0.14);
        float basin = smoothstep(0.92, 0.12, dist);
        float edge = smoothstep(0.3, 0.5, abs(vUV.x - 0.5)) + smoothstep(0.3, 0.5, abs(vUV.y - 0.5));

        float energy = basin * (swirl * 0.06 + cloud * 0.03);
        vec3 color = swirlColor * energy;
        color += vec3(0.02, 0.06, 0.14) * edge * 0.05;

        float alpha = energy * 0.55 + edge * 0.018;
        gl_FragColor = vec4(color, alpha);
      }
    `;

    const glowMat = new B.ShaderMaterial('energyFloorMat', this.scene, {
      vertex: 'energyFloor',
      fragment: 'energyFloor'
    }, {
      attributes: ['position', 'uv'],
      uniforms: ['worldViewProjection', 'time', 'swirlColor'],
      needAlphaBlending: true
    });
    glowMat.setFloat('time', 0);
    // Per-map hue for the ambient swirl (issue #182) — re-set by applyMapTheme.
    glowMat.setColor3('swirlColor', new B.Color3(...this.getPalette().swirl));
    glowMat.backFaceCulling = false;
    glowMat.alphaMode = B.Engine.ALPHA_ADD;
    this._floorGlowMat = glowMat;

    glow.material = glowMat;
    glow.isPickable = false;

    // Gate ONLY this glow overlay layer — the base textured ground mesh
    // above is the actual floor and stays on regardless of this setting.
    this.scene.registerBeforeRender(() => {
      const on = isEnabled('arenaAmbience', 'floorEnergyGlow');
      glow.setEnabled(on);
      if (on) glowMat.setFloat('time', performance.now() / 1000);
    });

    this._ground = ground;
    this._floorGlow = glow;
  }

  /**
   * @private Light shafts (issue #183b): three big additive gradient planes
   * aligned with the sun direction (engine's directional light points
   * (-0.4,-1,0.3), so the shafts lean up toward (0.358,0.894,-0.268) —
   * pitch ~0.466 rad, yaw ~2.214 rad). Very low alpha, frozen material, one
   * shared opacity texture; the only per-frame work is a slow position
   * drift inside the settings-gated ambient hook.
   */
  _createLightShafts() {
    const B = window.BABYLON;

    const tex = new B.DynamicTexture('lightShaftTex', { width: 64, height: 128 }, this.scene, false);
    const ctx = tex.getContext();
    ctx.clearRect(0, 0, 64, 128);
    const vGrad = ctx.createLinearGradient(0, 0, 0, 128);
    vGrad.addColorStop(0, 'rgba(255,255,255,0.9)');
    vGrad.addColorStop(0.75, 'rgba(255,255,255,0.35)');
    vGrad.addColorStop(1, 'rgba(255,255,255,0)');
    ctx.fillStyle = vGrad;
    ctx.fillRect(0, 0, 64, 128);
    const hGrad = ctx.createLinearGradient(0, 0, 64, 0);
    hGrad.addColorStop(0, 'rgba(255,255,255,0)');
    hGrad.addColorStop(0.25, 'rgba(255,255,255,1)');
    hGrad.addColorStop(0.75, 'rgba(255,255,255,1)');
    hGrad.addColorStop(1, 'rgba(255,255,255,0)');
    ctx.globalCompositeOperation = 'destination-in';
    ctx.fillStyle = hGrad;
    ctx.fillRect(0, 0, 64, 128);
    ctx.globalCompositeOperation = 'source-over';
    tex.update();
    tex.hasAlpha = true;
    tex.getAlphaFromRGB = true;
    this._shaftTex = tex;

    const mat = new B.StandardMaterial('lightShaftMat', this.scene);
    mat.diffuseColor = B.Color3.Black();
    mat.emissiveColor = new B.Color3(0.62, 0.58, 0.50); // sun-warm white
    mat.opacityTexture = tex;
    mat.disableLighting = true;
    mat.backFaceCulling = false;
    mat.alpha = 0.06;
    mat.alphaMode = B.Engine.ALPHA_ADD;
    mat.freeze();
    this._shaftMat = mat;

    const SHAFT_PITCH = 0.466;
    const SHAFT_YAW = 2.214;
    const defs = [
      { fx: 0.30, fz: 0.36, w: 130, h: 640, yawJitter: -0.12, phase: 0 },
      { fx: 0.58, fz: 0.55, w: 170, h: 700, yawJitter: 0.0, phase: 2.1 },
      { fx: 0.44, fz: 0.74, w: 110, h: 600, yawJitter: 0.14, phase: 4.2 },
    ];
    this._lightShafts = [];
    for (let i = 0; i < defs.length; i++) {
      const def = defs[i];
      const plane = B.MeshBuilder.CreatePlane(`lightShaft-${i}`, {
        width: def.w, height: def.h
      }, this.scene);
      const baseX = this.w * def.fx;
      const baseZ = this.h * def.fz;
      plane.position.set(baseX, 250, baseZ);
      plane.rotation.x = SHAFT_PITCH;
      plane.rotation.y = SHAFT_YAW + def.yawJitter;
      plane.material = mat;
      plane.isPickable = false;
      plane.applyFog = false; // additive haze must not be dimmed by depth fog
      this._lightShafts.push({ plane, baseX, baseZ, phase: def.phase });
    }

    // Slow drift + live settings check, same pattern as the other ambience
    // hooks in this file.
    this.scene.registerBeforeRender(() => {
      const on = isEnabled('arenaAmbience', 'lightShafts');
      for (const shaft of this._lightShafts) {
        shaft.plane.setEnabled(on);
        if (!on) continue;
        const t = performance.now() / 1000;
        shaft.plane.position.x = shaft.baseX + Math.sin(t * 0.05 + shaft.phase) * 26;
        shaft.plane.position.z = shaft.baseZ + Math.cos(t * 0.04 + shaft.phase) * 22;
      }
    });
  }

  /** @private Perimeter walls — thick energy barriers. */
  _createWalls() {
    const B = window.BABYLON;
    const wallH = 50, wallD = 20; // much thicker and taller

    // Semi-transparent energy wall material
    const wallMat = new B.StandardMaterial('wallMat', this.scene);
    wallMat.diffuseColor = new B.Color3(0.04, 0.08, 0.16);
    wallMat.emissiveColor = new B.Color3(0.05, 0.12, 0.26);
    wallMat.specularColor = new B.Color3(0.12, 0.24, 0.46);
    wallMat.alpha = 0.72;
    wallMat.backFaceCulling = false;
    wallMat.freeze();

    // Bright edge trim on top of walls. Emissive is palette-driven (issue
    // #182): applyMapTheme() unfreezes/retints it at round boundaries.
    const trimMat = new B.StandardMaterial('trimMat', this.scene);
    trimMat.diffuseColor = B.Color3.Black();
    trimMat.emissiveColor = new B.Color3(...this.getPalette().wallTrim);
    trimMat.disableLighting = true;
    trimMat.freeze();
    this._wallTrimMat = trimMat;

    const specs = [
      [this.w / 2, 0, this.w + wallD, wallD],
      [this.w / 2, this.h, this.w + wallD, wallD],
      [0, this.h / 2, wallD, this.h + wallD],
      [this.w, this.h / 2, wallD, this.h + wallD],
    ];
    // Collected so setupShadows() can register them as static casters.
    this._walls = [];
    for (let i = 0; i < specs.length; i++) {
      const [cx, cz, bw, bd] = specs[i];
      // Main wall body
      const wall = B.MeshBuilder.CreateBox(`wall-${i}`, {
        width: bw, height: wallH, depth: bd
      }, this.scene);
      wall.position.set(cx, wallH / 2, cz);
      wall.material = wallMat;
      wall.isPickable = false;
      markStaticMesh(wall);
      this._walls.push(wall);

      // Glowing trim strip on top
      const trim = B.MeshBuilder.CreateBox(`trim-${i}`, {
        width: bw + 2, height: 3, depth: bd + 2
      }, this.scene);
      trim.position.set(cx, wallH + 1.5, cz);
      trim.material = trimMat;
      trim.isPickable = false;
      markStaticMesh(trim);
    }
  }

  /** @private Corner pylons — glowing energy pillars with upward beams. */
  _createCornerPylons() {
    const B = window.BABYLON;
    const pylonH = 120;
    const corners = [
      [0, 0], [this.w, 0], [0, this.h], [this.w, this.h]
    ];

    // Pylon material — dark metal with blue emissive
    const pylonMat = new B.StandardMaterial('pylonMat', this.scene);
    pylonMat.diffuseColor = new B.Color3(0.08, 0.08, 0.12);
    pylonMat.emissiveColor = new B.Color3(0.05, 0.15, 0.3);
    pylonMat.specularColor = new B.Color3(0.3, 0.5, 0.8);
    pylonMat.freeze();

    // Beacon light material — bright blue glow
    const beaconMat = new B.StandardMaterial('beaconMat', this.scene);
    beaconMat.diffuseColor = B.Color3.Black();
    beaconMat.emissiveColor = new B.Color3(0.3, 0.7, 1.0);
    beaconMat.disableLighting = true;
    beaconMat.freeze();

    // Glow circle particle texture, shared across all 4 pylon beams; freed
    // once in dispose() rather than per-beam (ParticleSystem.dispose()
    // defaults to tearing down its texture, which would blank the rest).
    const glowTex = new B.DynamicTexture('pylonGlowTex', 32, this.scene, false);
    const gctx = glowTex.getContext();
    const grd = gctx.createRadialGradient(16, 16, 0, 16, 16, 16);
    grd.addColorStop(0, 'rgba(255,255,255,1)');
    grd.addColorStop(0.5, 'rgba(100,180,255,0.5)');
    grd.addColorStop(1, 'rgba(0,50,150,0)');
    gctx.fillStyle = grd;
    gctx.fillRect(0, 0, 32, 32);
    glowTex.update();
    glowTex.hasAlpha = true;
    this._pylonGlowTex = glowTex;

    this._pylons = [];

    for (let i = 0; i < corners.length; i++) {
      const [cx, cz] = corners[i];

      // Main pylon — octagonal-ish cylinder
      const pylon = B.MeshBuilder.CreateCylinder(`pylon-${i}`, {
        height: pylonH, diameter: 18, tessellation: 8
      }, this.scene);
      pylon.position.set(cx, pylonH / 2, cz);
      pylon.material = pylonMat;
      pylon.isPickable = false;
      markStaticMesh(pylon);

      // Beacon sphere at top
      const beacon = B.MeshBuilder.CreateSphere(`beacon-${i}`, {
        diameter: 12, segments: 8
      }, this.scene);
      beacon.position.set(cx, pylonH + 6, cz);
      beacon.material = beaconMat;
      beacon.isPickable = false;

      // Upward beam particles from the beacon
      const beam = new B.ParticleSystem(`pylonBeam-${i}`, 40, this.scene);
      beam.particleTexture = glowTex;
      beam.emitter = new B.Vector3(cx, pylonH + 10, cz);
      beam.minEmitBox = new B.Vector3(-2, 0, -2);
      beam.maxEmitBox = new B.Vector3(2, 0, 2);
      beam.direction1 = new B.Vector3(-0.1, 1, -0.1);
      beam.direction2 = new B.Vector3(0.1, 1, 0.1);
      beam.minEmitPower = 40;
      beam.maxEmitPower = 70;
      beam.minLifeTime = 1.0;
      beam.maxLifeTime = 2.0;
      beam.minSize = 5;
      beam.maxSize = 12;
      const PYLON_BEAM_EMIT_RATE = 15;
      beam.emitRate = PYLON_BEAM_EMIT_RATE;
      beam.gravity = new B.Vector3(0, 5, 0);
      beam.color1 = new B.Color4(0.2, 0.5, 1.0, 0.7);
      beam.color2 = new B.Color4(0.1, 0.3, 0.8, 0.4);
      beam.colorDead = new B.Color4(0.05, 0.15, 0.5, 0);
      beam.blendMode = B.ParticleSystem.BLENDMODE_ADD;
      beam.start();

      // Base ring glow
      const ring = B.MeshBuilder.CreateTorus(`pylonRing-${i}`, {
        diameter: 28, thickness: 2, tessellation: 24
      }, this.scene);
      ring.position.set(cx, 1, cz);
      ring.material = beaconMat;
      ring.isPickable = false;
      markStaticMesh(ring);

      this._pylons.push({ pylon, beacon, beam, ring, baseBeamEmitRate: PYLON_BEAM_EMIT_RATE });
    }

    // Animate beacon pulse + live settings check (beams, beacons, pylon
    // bodies, and base rings are one "corner pylons" toggle).
    let t = 0;
    this.scene.registerBeforeRender(() => {
      t += this.scene.getEngine().getDeltaTime() / 1000;
      const pulse = 0.7 + 0.3 * Math.sin(t * 2);
      const on = isEnabled('arenaAmbience', 'cornerPylons');
      for (const p of this._pylons) {
        p.pylon.setEnabled(on);
        p.beacon.setEnabled(on);
        p.ring.setEnabled(on);
        p.beam.emitRate = on ? p.baseBeamEmitRate : 0;
        if (!on) continue;
        p.beacon.scaling.setAll(pulse);
      }
    });
  }

  /** @private Anti-gravity thrusters under the arena — particle jets pushing down. */
  _createUndersideThrusters() {
    const B = window.BABYLON;

    // Glow texture for thruster particles, shared across all jets; see
    // note on glowTex above about deferring texture disposal.
    const thrustTex = new B.DynamicTexture('thrustTex', 32, this.scene, false);
    const tctx = thrustTex.getContext();
    const tgrd = tctx.createRadialGradient(16, 16, 0, 16, 16, 16);
    tgrd.addColorStop(0, 'rgba(255,200,100,1)');
    tgrd.addColorStop(0.3, 'rgba(255,120,50,0.6)');
    tgrd.addColorStop(0.7, 'rgba(100,50,200,0.3)');
    tgrd.addColorStop(1, 'rgba(50,20,100,0)');
    tctx.fillStyle = tgrd;
    tctx.fillRect(0, 0, 32, 32);
    thrustTex.update();
    thrustTex.hasAlpha = true;
    this._thrusterGlowTex = thrustTex;

    // Place 4 thrusters in a diamond pattern under the arena
    const positions = [
      [this.w * 0.25, this.h * 0.5],
      [this.w * 0.75, this.h * 0.5],
      [this.w * 0.5, this.h * 0.25],
      [this.w * 0.5, this.h * 0.75],
    ];

    this._thrusters = [];

    for (let i = 0; i < positions.length; i++) {
      const [px, pz] = positions[i];

      // Thruster nozzle cone (under the floor, pointing down)
      const nozzle = B.MeshBuilder.CreateCylinder(`thruster-${i}`, {
        height: 15, diameterTop: 10, diameterBottom: 35, tessellation: 12
      }, this.scene);
      nozzle.position.set(px, -12, pz);
      const hMat = new B.StandardMaterial(`thrusterMat-${i}`, this.scene);
      hMat.diffuseColor = new B.Color3(0.1, 0.1, 0.15);
      hMat.emissiveColor = new B.Color3(0.15, 0.05, 0.02);
      hMat.freeze();
      nozzle.material = hMat;
      nozzle.isPickable = false;
      markStaticMesh(nozzle);

      // Inner glow ring at nozzle opening
      const glow = B.MeshBuilder.CreateTorus(`thrusterGlow-${i}`, {
        diameter: 30, thickness: 3, tessellation: 16
      }, this.scene);
      glow.position.set(px, -20, pz);
      const glowMat = new B.StandardMaterial(`thrustGlowMat-${i}`, this.scene);
      glowMat.diffuseColor = B.Color3.Black();
      glowMat.emissiveColor = new B.Color3(1.0, 0.5, 0.1);
      glowMat.disableLighting = true;
      glowMat.freeze();
      glow.material = glowMat;
      glow.isPickable = false;
      markStaticMesh(glow);

      // Downward particle jet — emits from BELOW the nozzle
      const jet = new B.ParticleSystem(`thrusterJet-${i}`, 80, this.scene);
      jet.particleTexture = thrustTex;
      jet.emitter = new B.Vector3(px, -22, pz);
      jet.minEmitBox = new B.Vector3(-6, 0, -6);
      jet.maxEmitBox = new B.Vector3(6, 0, 6);
      jet.direction1 = new B.Vector3(-0.2, -1, -0.2);
      jet.direction2 = new B.Vector3(0.2, -1, 0.2);
      jet.minEmitPower = 40;
      jet.maxEmitPower = 80;
      jet.minLifeTime = 0.4;
      jet.maxLifeTime = 1.0;
      jet.minSize = 10;
      jet.maxSize = 25;
      const THRUSTER_JET_EMIT_RATE = 35;
      jet.emitRate = THRUSTER_JET_EMIT_RATE;
      jet.gravity = new B.Vector3(0, -15, 0);
      jet.color1 = new B.Color4(1.0, 0.6, 0.2, 0.9);
      jet.color2 = new B.Color4(0.6, 0.3, 0.8, 0.5);
      jet.colorDead = new B.Color4(0.3, 0.1, 0.4, 0);
      jet.blendMode = B.ParticleSystem.BLENDMODE_ADD;
      jet.start();

      this._thrusters.push({ nozzle, glow, jet, baseJetEmitRate: THRUSTER_JET_EMIT_RATE });
    }

    // Live settings check — nozzle housing, glow ring, and jet particles
    // are one "underside thrusters" toggle.
    this.scene.registerBeforeRender(() => {
      const on = isEnabled('arenaAmbience', 'thrusters');
      for (const t of this._thrusters) {
        t.nozzle.setEnabled(on);
        t.glow.setEnabled(on);
        t.jet.emitRate = on ? t.baseJetEmitRate : 0;
      }
    });
  }

  /** @private Energy waterfalls cascading off the arena edges. */
  _createEdgeWaterfalls() {
    const B = window.BABYLON;

    // Particle texture — soft blue glow, shared across all waterfalls; see
    // note on glowTex above about deferring texture disposal.
    const dropTex = new B.DynamicTexture('dropTex', 16, this.scene, false);
    const dctx = dropTex.getContext();
    const dgrd = dctx.createRadialGradient(8, 8, 0, 8, 8, 8);
    dgrd.addColorStop(0, 'rgba(100,180,255,1)');
    dgrd.addColorStop(0.5, 'rgba(50,100,200,0.5)');
    dgrd.addColorStop(1, 'rgba(20,50,150,0)');
    dctx.fillStyle = dgrd;
    dctx.fillRect(0, 0, 16, 16);
    dropTex.update();
    dropTex.hasAlpha = true;
    this._waterfallDropTex = dropTex;

    // One waterfall per edge, emitting from the wall top and falling down
    const edges = [
      { pos: [this.w / 2, 0],   box: [this.w * 0.8, 0, 0, 2] },  // north
      { pos: [this.w / 2, this.h], box: [this.w * 0.8, 0, 0, 2] },  // south
      { pos: [0, this.h / 2],   box: [0, 0, 2, this.h * 0.8] },  // west
      { pos: [this.w, this.h / 2], box: [0, 0, 2, this.h * 0.8] },  // east
    ];

    this._waterfalls = [];
    const wallH = 50;
    const WATERFALL_EMIT_RATE = 20;

    for (let i = 0; i < edges.length; i++) {
      const e = edges[i];
      const ps = new B.ParticleSystem(`waterfall-${i}`, 100, this.scene);
      ps.particleTexture = dropTex;
      ps.emitter = new B.Vector3(e.pos[0], wallH - 5, e.pos[1]);
      ps.minEmitBox = new B.Vector3(-e.box[0]/2, -2, -e.box[3]/2);
      ps.maxEmitBox = new B.Vector3(e.box[0]/2, 2, e.box[3]/2);
      ps.direction1 = new B.Vector3(-0.1, -1, -0.1);
      ps.direction2 = new B.Vector3(0.1, -0.5, 0.1);
      ps.minEmitPower = 5;
      ps.maxEmitPower = 15;
      ps.minLifeTime = 1.5;
      ps.maxLifeTime = 3.0;
      ps.minSize = 2;
      ps.maxSize = 6;
      ps.emitRate = WATERFALL_EMIT_RATE;
      ps.gravity = new B.Vector3(0, -20, 0);
      ps.color1 = new B.Color4(0.2, 0.5, 1.0, 0.6);
      ps.color2 = new B.Color4(0.1, 0.3, 0.8, 0.3);
      ps.colorDead = new B.Color4(0.05, 0.15, 0.5, 0);
      ps.blendMode = B.ParticleSystem.BLENDMODE_ADD;
      ps.start();
      this._waterfalls.push(ps);
    }

    // Live settings check — zero the emit rate rather than stop() so a
    // toggle takes effect immediately without losing emitter state.
    this.scene.registerBeforeRender(() => {
      const rate = isEnabled('arenaAmbience', 'edgeWaterfalls') ? WATERFALL_EMIT_RATE : 0;
      for (const ps of this._waterfalls) ps.emitRate = rate;
    });
  }

  /** @private Rotating energy rings around corner pylons. */
  _createPylonRings() {
    const B = window.BABYLON;
    const corners = [
      [0, 0], [this.w, 0], [0, this.h], [this.w, this.h]
    ];

    const ringMat = new B.StandardMaterial('pylonRingMat', this.scene);
    ringMat.diffuseColor = B.Color3.Black();
    ringMat.emissiveColor = new B.Color3(0.15, 0.45, 0.9);
    ringMat.alpha = 0.5;
    ringMat.disableLighting = true;
    ringMat.backFaceCulling = false;
    ringMat.freeze();

    this._pylonRings = [];

    for (let i = 0; i < corners.length; i++) {
      const [cx, cz] = corners[i];

      // Two rings per pylon at different heights, rotating opposite directions
      const ring1 = B.MeshBuilder.CreateTorus(`pylonOrbit1-${i}`, {
        diameter: 50, thickness: 1.5, tessellation: 32
      }, this.scene);
      ring1.position.set(cx, 60, cz);
      ring1.material = ringMat;
      ring1.isPickable = false;

      const ring2 = B.MeshBuilder.CreateTorus(`pylonOrbit2-${i}`, {
        diameter: 40, thickness: 1, tessellation: 24
      }, this.scene);
      ring2.position.set(cx, 90, cz);
      ring2.rotation.x = 0.4;
      ring2.material = ringMat;
      ring2.isPickable = false;

      this._pylonRings.push({ ring1, ring2, cx, cz });
    }

    // Animate rotation + live settings check. Not its own toggle in the
    // schema — these orbit rings decorate the same corner-pylon structures
    // gated by _createCornerPylons(), so they ride the 'cornerPylons' key
    // too (leaving them on while the pylons vanish would look broken).
    this.scene.registerBeforeRender(() => {
      const on = isEnabled('arenaAmbience', 'cornerPylons');
      const dt = this.scene.getEngine().getDeltaTime() / 1000;
      for (const pr of this._pylonRings) {
        pr.ring1.setEnabled(on);
        pr.ring2.setEnabled(on);
        if (!on) continue;
        pr.ring1.rotation.y += dt * 0.8;
        pr.ring1.rotation.x = Math.sin(pr.ring1.rotation.y) * 0.3;
        pr.ring2.rotation.y -= dt * 1.2;
      }
    });
  }

  /** @private Holographic arena title floating above. */
  _createHoloTitle() {
    const B = window.BABYLON;

    // Create text texture
    const texW = 1024, texH = 128;
    const tex = new B.DynamicTexture('holoTitleTex', { width: texW, height: texH }, this.scene, false);
    tex.hasAlpha = true;
    const ctx = tex.getContext();
    ctx.clearRect(0, 0, texW, texH);
    ctx.font = 'bold 72px monospace';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    // Outer glow
    ctx.shadowColor = 'rgba(50,150,255,0.8)';
    ctx.shadowBlur = 20;
    ctx.fillStyle = 'rgba(100,200,255,0.9)';
    ctx.fillText('AI BATTLE ARENA', texW/2, texH/2);
    // Second pass for brightness
    ctx.shadowBlur = 10;
    ctx.fillStyle = 'rgba(200,230,255,0.7)';
    ctx.fillText('AI BATTLE ARENA', texW/2, texH/2);
    tex.update();

    // Sized and raised to clear the corner-pylon beams at overview zoom.
    const plane = B.MeshBuilder.CreatePlane('holoTitle', {
      width: 520, height: 65
    }, this.scene);
    plane.position.set(this.w / 2, 200, this.h / 2);
    plane.billboardMode = B.Mesh.BILLBOARDMODE_ALL;
    plane.isPickable = false;
    plane.applyFog = false; // floating signage reads at any zoom, unfogged

    const mat = new B.StandardMaterial('holoTitleMat', this.scene);
    mat.diffuseTexture = tex;
    mat.emissiveTexture = tex;
    mat.emissiveColor = new B.Color3(0.5, 0.7, 1.0);
    mat.opacityTexture = tex;
    mat.disableLighting = true;
    mat.backFaceCulling = false;
    mat.alpha = 0.85;
    plane.material = mat;

    // Subtle float animation (observer stored so dispose() can remove it —
    // the arena-resize rebuild path must not leak per-frame work).
    const baseY = 200;
    this._holoTitleObs = this.scene.onBeforeRenderObservable.add(() => {
      // Portrait phones: Babylon's vertical-fixed FOV shows only ~380 world
      // units of width at the default zoom, so this 520-unit banner would
      // span well past both screen edges. Landscape/desktop only.
      const eng = this.scene.getEngine();
      const on = isEnabled('arenaAmbience', 'holoTitle') &&
        eng.getRenderWidth() >= eng.getRenderHeight();
      plane.setEnabled(on);
      if (!on) return;
      const t = performance.now() / 1000;
      plane.position.y = baseY + Math.sin(t * 0.5) * 5;
      // Subtle pulse
      mat.alpha = 0.75 + 0.1 * Math.sin(t * 1.5);
    });

    this._holoTitle = plane;
    this._holoTitleTex = tex;
    this._holoTitleMat = mat;
  }

  /** @private Ambient floating dust/ember particles for atmosphere. */
  _createAmbientParticles() {
    const B = window.BABYLON;
    const ps = new B.ParticleSystem('ambientParticles', 25, this.scene);

    // Soft white dot, canvas-drawn. The previously embedded PNG data URI was
    // truncated (invalid base64 length, stream ends mid-chunk) and failed to
    // decode in every browser — Firefox logged "Image corrupt or truncated"
    // and the particles sampled an incomplete texture.
    const dotSize = 32;
    const dotCanvas = document.createElement('canvas');
    dotCanvas.width = dotSize;
    dotCanvas.height = dotSize;
    const dotCtx = dotCanvas.getContext('2d');
    const dotGrad = dotCtx.createRadialGradient(
      dotSize / 2, dotSize / 2, 0, dotSize / 2, dotSize / 2, dotSize / 2,
    );
    dotGrad.addColorStop(0, 'rgba(255,255,255,1)');
    dotGrad.addColorStop(0.5, 'rgba(255,255,255,0.55)');
    dotGrad.addColorStop(1, 'rgba(255,255,255,0)');
    dotCtx.fillStyle = dotGrad;
    dotCtx.fillRect(0, 0, dotSize, dotSize);
    const dotTex = new B.DynamicTexture(
      'ambientParticleTex', { width: dotSize, height: dotSize }, this.scene, false,
    );
    dotTex.hasAlpha = true;
    dotTex.getContext().drawImage(dotCanvas, 0, 0);
    dotTex.update();
    ps.particleTexture = dotTex;

    // Box emitter covering the arena center area
    const emitW = Math.min(this.w, 1500) * 0.5;
    const emitH = Math.min(this.h, 1500) * 0.5;
    ps.emitter = new B.Vector3(this.w / 2, 5, this.h / 2);
    ps.minEmitBox = new B.Vector3(-emitW, 0, -emitH);
    ps.maxEmitBox = new B.Vector3(emitW, 40, emitH);

    // Small particles
    ps.minSize = 0.5;
    ps.maxSize = 1.5;

    // Slow upward movement
    ps.minEmitPower = 1;
    ps.maxEmitPower = 3;
    ps.direction1 = new B.Vector3(-0.3, 1, -0.3);
    ps.direction2 = new B.Vector3(0.3, 1, 0.3);

    // Long lifetime
    ps.minLifeTime = 3;
    ps.maxLifeTime = 6;

    // Emission rate — slow and sparse
    const AMBIENT_EMIT_RATE = 5;
    ps.emitRate = AMBIENT_EMIT_RATE;

    // Warm amber/orange fading to transparent
    ps.color1 = new B.Color4(1.0, 0.7, 0.3, 0.4);
    ps.color2 = new B.Color4(1.0, 0.5, 0.2, 0.2);
    ps.colorDead = new B.Color4(1.0, 0.3, 0.1, 0.0);

    // Additive blending for glow effect
    ps.blendMode = B.ParticleSystem.BLENDMODE_ADD;

    // Slight gravity pull to slow upward drift
    ps.gravity = new B.Vector3(0, -0.5, 0);

    ps.start();

    // Live settings check — zero the emit rate rather than stop() so a
    // toggle takes effect immediately without losing emitter state.
    this.scene.registerBeforeRender(() => {
      ps.emitRate = isEnabled('arenaAmbience', 'ambientParticles') ? AMBIENT_EMIT_RATE : 0;
    });

    this._ambientParticles = ps;
  }

  /** @private Create reusable materials for zone rings. */
  _initZoneMaterials() {
    const B = window.BABYLON;
    // Current zone boundary — electric blue
    this._zoneMat = new B.StandardMaterial('zoneRingMat', this.scene);
    this._zoneMat.emissiveColor = new B.Color3(0.1, 0.5, 1.0);
    this._zoneMat.diffuseColor = new B.Color3(0, 0, 0);
    this._zoneMat.specularColor = B.Color3.Black();
    this._zoneMat.disableLighting = true;
    this._zoneMat.alpha = ZONE_RING_BASE_ALPHA;
    this._zoneMat.backFaceCulling = false;

    // Target zone — dim white
    this._targetMat = new B.StandardMaterial('targetRingMat', this.scene);
    this._targetMat.emissiveColor = new B.Color3(1.0, 1.0, 1.0);
    this._targetMat.diffuseColor = new B.Color3(0, 0, 0);
    this._targetMat.specularColor = B.Color3.Black();
    this._targetMat.disableLighting = true;
    this._targetMat.alpha = 0.25;
    this._targetMat.backFaceCulling = false;

    // Clip planes bounding the arena rectangle. The zone can start larger
    // than the map (circumscribing it), so any ring geometry outside the
    // walls is clipped away — the circle only becomes visible once it
    // shrinks inside the arena.
    this._mapClipPlanes = [
      new B.Plane(-1, 0, 0, 0),        // discard x < 0
      new B.Plane(1, 0, 0, -this.w),   // discard x > w
      new B.Plane(0, 0, -1, 0),        // discard z < 0
      new B.Plane(0, 0, 1, -this.h),   // discard z > h
    ];
  }

  /** @private Clip a mesh to the arena rectangle using scene clip planes. */
  _clipToArena(mesh) {
    mesh.onBeforeRenderObservable.add(() => {
      const s = this.scene;
      const p = this._mapClipPlanes;
      s.clipPlane = p[0]; s.clipPlane2 = p[1];
      s.clipPlane3 = p[2]; s.clipPlane4 = p[3];
    });
    mesh.onAfterRenderObservable.add(() => {
      const s = this.scene;
      s.clipPlane = null; s.clipPlane2 = null;
      s.clipPlane3 = null; s.clipPlane4 = null;
    });
  }

  /**
   * Meshes the engine's GlowLayer must skip (issue #181). The skybox, ground
   * deck, and energy-floor swirl are frame-filling emissive art already tuned
   * against bloom — feeding them to the glow pass hazes the whole arena. The
   * translucent wall bodies are excluded too so only their trim strips glow.
   */
  /** Store the glow layer so lazily-created meshes (zone rings) can exclude
   *  themselves at creation time — the setup-time exclusion list below only
   *  covers meshes that already exist. */
  setGlowLayer(glow) {
    this._glowLayer = glow;
  }

  getGlowExcludedMeshes() {
    const meshes = [this._skybox, this._ground, this._floorGlow];
    if (this._walls) meshes.push(...this._walls);
    // Light shafts are already additive haze — glowing them doubles it.
    if (this._lightShafts) meshes.push(...this._lightShafts.map((s) => s.plane));
    // Zone rings are clipped to the arena rect via material clip planes, but
    // the glow pass renders its own copy of glowing meshes WITHOUT those clip
    // planes — the ring's halo drew far outside the arena edge (user-reported
    // 2026-07-18). Excluded until the glow pass respects clipping.
    meshes.push(this._zoneRing, this._targetRing);
    return meshes.filter(Boolean);
  }

  /** Set up shadow generator on the directional light. */
  setupShadows(light) {
    const B = window.BABYLON;
    // 2048 instead of 1024: every caster is static and the light never
    // moves, so the map below is baked once (REFRESHRATE_RENDER_ONCE), not
    // re-rendered per frame — the higher resolution is free at runtime.
    const shadowGen = new B.ShadowGenerator(2048, light);
    shadowGen.usePercentageCloserFiltering = true;
    shadowGen.bias = 0.005;
    shadowGen.normalBias = 0.02;
    shadowGen.setDarkness(0.55);
    shadowGen.getShadowMap().refreshRate = B.RenderTargetTexture.REFRESHRATE_RENDER_ONCE;
    // The boundary walls are static geometry too — let them cast.
    if (this._walls) {
      for (const wall of this._walls) shadowGen.addShadowCaster(wall);
    }
    // Ground receives shadows
    if (this._ground) {
      this._ground.receiveShadows = true;
    }
    // Live settings check — the shadowEnabled setter early-returns when the
    // value is unchanged, so a per-frame assignment costs the same as the
    // other ambience gates above.
    this.scene.registerBeforeRender(() => {
      light.shadowEnabled = isEnabled('rendering', 'shadows');
    });
    this._shadowGen = shadowGen;
  }

  /** Re-bake the frozen shadow map. Call whenever static casters change
   *  (e.g. the obstacle rebuild at a round boundary). */
  refreshShadows() {
    this._shadowGen?.getShadowMap()?.resetRefreshCounter();
  }

  /** Register a mesh as a shadow caster. */
  addShadowCaster(mesh) {
    if (this._shadowGen) {
      this._shadowGen.addShadowCaster(mesh);
    }
  }

  /**
   * Update safe zone visualization.
   * @param {Object|null} safeZone - { center: [x,y], radius, target_center: [x,y], target_radius }
   * @param {boolean} [suddenDeath] - tint the zone ring red while sudden death is active
   */
  update(safeZone, suddenDeath = false) {
    if (!safeZone) return;

    // Sudden-death tint — menacing red vs the usual electric blue. Mutate
    // the shared Color3 in place (no per-call alloc), only on transitions.
    const sd = !!suddenDeath;
    if (this._zoneSuddenDeath !== sd) {
      this._zoneSuddenDeath = sd;
      if (sd) this._zoneMat.emissiveColor.set(1.0, 0.18, 0.24);
      else this._zoneMat.emissiveColor.set(0.1, 0.5, 1.0);
    }

    const cx = safeZone.center[0];
    const cy = safeZone.center[1];
    const r = safeZone.radius;

    // A radius jump UP past the previous target is a new round (the zone
    // only shrinks during a round) — snap the ring instead of lerping across
    // the round boundary and record the round's OPENING state so the
    // intermission show can play the shrink in reverse (issue #192).
    const newRound = this._zoneCurR === undefined || r > this._zoneTargetR + 1;

    // Store target values for smooth lerping
    this._zoneTargetR = r;
    this._zoneTargetCx = cx;
    this._zoneTargetCy = cy;

    if (newRound) {
      this._zoneCurR = r;
      this._zoneCurCx = cx;
      this._zoneCurCy = cy;
      this._zoneRoundStartR = r;
      this._zoneRoundStartCx = cx;
      this._zoneRoundStartCy = cy;
    }
    // Live zone state re-arms a ring the intermission choreography hid (no
    // arena_state flows between rounds, so this can't fight the rewind).
    this._zoneRewind = null;
    this._zoneRingHidden = false;

    // Ensure ring meshes exist
    this._ensureZoneRing();
    if (safeZone.target_center) {
      this._buildTargetRing(
        safeZone.target_center[0], safeZone.target_center[1],
        safeZone.target_radius || 75
      );
    }

    this._ensureZoneObserver();
  }

  /** @private Register the per-frame zone-ring animation hook once. Drives
   *  the normal shrink lerp plus the intermission choreography (rewind of
   *  the blue ring, glide of the target ring). */
  _ensureZoneObserver() {
    if (this._zoneObserverRegistered) return;
    this._zoneObserverRegistered = true;
    this.scene.registerBeforeRender(() => {
      // dt-based smoothing so convergence speed is framerate-independent
      // (a fixed per-frame factor converges 2.4x faster at 144Hz than 60Hz).
      const dt = Math.min(this.scene.getEngine().getDeltaTime() / 1000, 0.1);
      this._tickZoneRing(dt);
      this._tickTargetRingGlide(dt);
    });
  }

  /** @private Per-frame blue safe-zone ring state: hidden > rewind > lerp. */
  _tickZoneRing(dt) {
    if (!this._zoneRing) return;
    if (this._zoneRingHidden) {
      // Hidden between the intermission rewind and the new round's first
      // zone state (update() above re-arms it).
      this._zoneRing.setEnabled(false);
      return;
    }
    this._zoneRing.setEnabled(true);
    // This ring is functionally informative (shows the shrinking safe
    // zone), not pure decoration, but still user-toggleable. Gate via
    // alpha rather than setEnabled so a spectator relying on it can
    // toggle it back on mid-round without any state loss.
    const baseAlpha = isEnabled('gameplayZoneIndicators', 'safeZoneRing') ? ZONE_RING_BASE_ALPHA : 0;
    const rw = this._zoneRewind;
    if (rw) {
      // Intermission rewind (issue #192): play the round's shrink backwards
      // to the recorded opening state, dissolving through the tail, then
      // stay hidden until the new round's state re-arms the ring. When the
      // zone never shrank the interpolation is a no-op and this is the
      // documented clean fade-out fallback.
      rw.t = Math.min(rw.dur, rw.t + dt);
      const p = rw.t / rw.dur;
      const e = 1 - (1 - p) * (1 - p); // easeOutQuad — expansion settles in
      this._zoneCurR = rw.fromR + (rw.toR - rw.fromR) * e;
      this._zoneCurCx = rw.fromCx + (rw.toCx - rw.fromCx) * e;
      this._zoneCurCy = rw.fromCy + (rw.toCy - rw.fromCy) * e;
      const FADE_START = 0.65;
      const fade = p < FADE_START ? 1 : 1 - (p - FADE_START) / (1 - FADE_START);
      this._zoneMat.alpha = baseAlpha * Math.max(0, fade);
      this._zoneRing.scaling.set(this._zoneCurR * 2, this._zoneCurR * 2, this._zoneCurR * 2);
      this._zoneRing.position.set(this._zoneCurCx, 2, this._zoneCurCy);
      if (p >= 1) {
        this._zoneRewind = null;
        this._zoneRingHidden = true;
        this._zoneMat.alpha = baseAlpha;
      }
      return;
    }
    if (this._zoneTargetR === undefined) return;
    this._zoneMat.alpha = baseAlpha;
    const lerpSpeed = 1 - Math.exp(-5 * dt);
    this._zoneCurR += (this._zoneTargetR - this._zoneCurR) * lerpSpeed;
    this._zoneCurCx += (this._zoneTargetCx - this._zoneCurCx) * lerpSpeed;
    this._zoneCurCy += (this._zoneTargetCy - this._zoneCurCy) * lerpSpeed;
    this._zoneRing.scaling.set(this._zoneCurR * 2, this._zoneCurR * 2, this._zoneCurR * 2);
    this._zoneRing.position.set(this._zoneCurCx, 2, this._zoneCurCy);
  }

  /** @private Per-frame target-ring glide toward the announced next-round
   *  placement (issue #192). Ends exactly on the destination, so the new
   *  round's first _buildTargetRing is a visual no-op. */
  _tickTargetRingGlide(dt) {
    const g = this._targetGlide;
    if (!g || !this._targetRing) return;
    g.t = Math.min(g.dur, g.t + dt);
    const p = g.t / g.dur;
    const e = p * p * (3 - 2 * p); // smoothstep — eases out of rest, into rest
    const r = g.fromR + (g.toR - g.fromR) * e;
    this._targetRing.scaling.set(r * 2, r * 2, r * 2);
    this._targetRing.position.set(
      g.fromCx + (g.toCx - g.fromCx) * e,
      1,
      g.fromCy + (g.toCy - g.fromCy) * e,
    );
    if (p >= 1) this._targetGlide = null;
  }

  /**
   * Intermission choreography (issue #192): animate the blue safe-zone ring
   * back out to the radius/centre it started the round with — the shrink
   * played in reverse — fading through the tail, then keep it hidden until
   * the next round's zone state arrives. Returns false when there is no
   * ring/state to rewind (the show simply skips the beat).
   * @param {number} durationSecs
   */
  beginZoneRingRewind(durationSecs) {
    if (!this._zoneRing || this._zoneCurR === undefined || this._zoneRingHidden) return false;
    this._zoneRewind = {
      t: 0,
      dur: Math.max(0.5, Number(durationSecs) || 2),
      fromR: this._zoneCurR,
      fromCx: this._zoneCurCx,
      fromCy: this._zoneCurCy,
      toR: this._zoneRoundStartR !== undefined ? this._zoneRoundStartR : this._zoneCurR,
      toCx: this._zoneRoundStartCx !== undefined ? this._zoneRoundStartCx : this._zoneCurCx,
      toCy: this._zoneRoundStartCy !== undefined ? this._zoneRoundStartCy : this._zoneCurCy,
    };
    this._ensureZoneObserver();
    return true;
  }

  /** Current target-ring placement, or null before its first build. The
   *  intermission director snapshots this before a stage resize so the
   *  glide can still start from the old round's spot on the fresh scene. */
  getZoneTargetRingState() {
    if (!this._targetRing) return null;
    return {
      cx: this._targetRing.position.x,
      cy: this._targetRing.position.z,
      r: this._targetRing.scaling.x / 2,
    };
  }

  /**
   * Intermission choreography (issue #192): glide the target ring from where
   * the old round left it to the NEXT round's announced placement, timed to
   * land before the round starts. `from` (optional {cx,cy,r}) seeds a ring
   * that does not exist yet — e.g. right after a mid-show stage resize; with
   * neither a ring nor a seed the ring is simply parked at the destination
   * so the first keyframe is still a visual no-op.
   * @param {{cx:number,cy:number,r:number}} to
   * @param {number} durationSecs
   * @param {{cx:number,cy:number,r:number}|null} [from]
   */
  glideZoneTargetRing(to, durationSecs, from = null) {
    if (!to) return false;
    if (!this._targetRing && from) this._buildTargetRing(from.cx, from.cy, from.r);
    if (!this._targetRing) {
      this._buildTargetRing(to.cx, to.cy, to.r);
      return false;
    }
    this._targetGlide = {
      t: 0,
      dur: Math.max(0.5, Number(durationSecs) || 2),
      fromCx: this._targetRing.position.x,
      fromCy: this._targetRing.position.z,
      fromR: this._targetRing.scaling.x / 2,
      toCx: to.cx,
      toCy: to.cy,
      toR: Math.max(0.5, Number(to.r) || 0.5),
    };
    this._ensureZoneObserver();
    return true;
  }

  /** Snap any live intermission ring choreography to its end state (show
   *  fast-forward): a mid-rewind blue ring hides, a mid-glide target ring
   *  lands on its destination. The next zone update restores everything. */
  settleZoneChoreography() {
    if (this._zoneRewind) {
      this._zoneRewind = null;
      this._zoneRingHidden = true;
      if (this._zoneRing) this._zoneRing.setEnabled(false);
    }
    const g = this._targetGlide;
    if (g && this._targetRing) {
      this._targetGlide = null;
      this._targetRing.scaling.set(g.toR * 2, g.toR * 2, g.toR * 2);
      this._targetRing.position.set(g.toCx, 1, g.toCy);
    }
  }

  /** @private Ensure zone ring mesh exists. */
  _ensureZoneRing() {
    const B = window.BABYLON;
    if (!this._zoneRing) {
      this._zoneRing = B.MeshBuilder.CreateTorus('zoneRing', {
        diameter: 1, thickness: 0.005, tessellation: ZONE_RING_SEGMENTS
      }, this.scene);
      this._zoneRing.material = this._zoneMat;
      this._clipToArena(this._zoneRing);
      // The glow pass ignores material clip planes; an unexcluded ring
      // halos far outside the arena edge (see getGlowExcludedMeshes).
      if (this._glowLayer) this._glowLayer.addExcludedMesh(this._zoneRing);
    }
  }

  /** @private Build or reposition the target zone ring. */
  _buildTargetRing(cx, cy, r) {
    const B = window.BABYLON;
    // Server state is authoritative — a live glide yields to it.
    this._targetGlide = null;
    if (!this._targetRing) {
      this._targetRing = B.MeshBuilder.CreateTorus('targetRing', {
        diameter: 1, thickness: 0.003, tessellation: ZONE_RING_SEGMENTS
      }, this.scene);
      this._targetRing.material = this._targetMat;
      this._clipToArena(this._targetRing);
      if (this._glowLayer) this._glowLayer.addExcludedMesh(this._targetRing);
    }
    this._targetRing.scaling.set(r * 2, r * 2, r * 2);
    this._targetRing.position.set(cx, 1, cy);
  }

  /** Dispose all environment resources. */
  dispose() {
    if (this._spaceObjects) {
      for (const obj of this._spaceObjects) {
        obj.plane.dispose(); obj.mat.dispose();
        if (obj.trail) obj.trail.dispose(false);
      }
      this._spaceObjects = null;
    }
    if (this._cometTrailTex) { this._cometTrailTex.dispose(); this._cometTrailTex = null; }
    if (this._pylons) {
      for (const p of this._pylons) { p.pylon.dispose(); p.beacon.dispose(); p.beam.dispose(false); p.ring.dispose(); }
      this._pylons = null;
    }
    if (this._pylonGlowTex) { this._pylonGlowTex.dispose(); this._pylonGlowTex = null; }
    if (this._thrusters) {
      for (const t of this._thrusters) { t.nozzle.dispose(); t.glow.dispose(); t.jet.dispose(false); }
      this._thrusters = null;
    }
    if (this._thrusterGlowTex) { this._thrusterGlowTex.dispose(); this._thrusterGlowTex = null; }
    if (this._waterfalls) {
      for (const w of this._waterfalls) w.dispose(false);
      this._waterfalls = null;
    }
    if (this._waterfallDropTex) { this._waterfallDropTex.dispose(); this._waterfallDropTex = null; }
    if (this._pylonRings) {
      for (const pr of this._pylonRings) { pr.ring1.dispose(); pr.ring2.dispose(); }
      this._pylonRings = null;
    }

    if (this._lightShafts) {
      for (const shaft of this._lightShafts) shaft.plane.dispose();
      this._lightShafts = null;
    }
    if (this._shaftMat) { this._shaftMat.dispose(); this._shaftMat = null; }
    if (this._shaftTex) { this._shaftTex.dispose(); this._shaftTex = null; }

    if (this._floorGlow) { this._floorGlow.dispose(); this._floorGlow = null; }
    if (this._ambientParticles) { this._ambientParticles.dispose(); this._ambientParticles = null; }

    if (this._zoneRing) { this._zoneRing.dispose(); this._zoneRing = null; }
    if (this._targetRing) { this._targetRing.dispose(); this._targetRing = null; }
    if (this._shadowGen) { this._shadowGen.dispose(); this._shadowGen = null; }
    if (this._skybox) { this._skybox.dispose(); this._skybox = null; }

    if (this._holoTitle) {
      if (this._holoTitleObs) {
        this.scene.onBeforeRenderObservable.remove(this._holoTitleObs);
        this._holoTitleObs = null;
      }
      this._holoTitleTex.dispose();
      this._holoTitleMat.dispose();
      this._holoTitle.dispose();
      this._holoTitle = null;
      this._holoTitleTex = null;
      this._holoTitleMat = null;
    }
  }
}
