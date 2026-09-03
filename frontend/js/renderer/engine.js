'use strict';

/**
 * Babylon.js engine — scene setup, render loop, module orchestration.
 * @module renderer/engine
 */

import { CameraController } from './camera.js?v=20260718b';
import { BotRenderer } from './bots.js?v=20260718o';
import { EnvironmentRenderer } from './environment.js?v=20260903a';
import { ObstacleRenderer } from './obstacles.js?v=20260903a';
import { IntermissionDirector } from './intermission-director.js?v=20260718h';
import { PickupRenderer } from './pickups.js?v=20260714f';
import { EffectRenderer } from './effects.js?v=20260718c';
import { TrailRenderer } from './trails.js?v=20260714e';
import { ProjectileRenderer } from './projectiles.js?v=20260711a';
import { GameplayRenderer } from './gameplay.js?v=20260718i';
import { getState, isEnabled, onSettingsChange } from '../settings.js';

// Bot positions are smoothed via exponential lerp each frame,
// so no tick-interval-based alpha is needed.

const WEBGPU_PROBE_TIMEOUT_MS = 1500;

/**
 * Dynamic mode grading (issue #183c): eases the existing pipeline's
 * imageProcessing values with game state — sudden death pulls the frame
 * toward a red-vignetted, higher-contrast look, a round win pulses warm
 * exposure, the lobby rests slightly softer, and the followed-bot damage
 * vignette (issue #184b) composes additively on top. No new post passes,
 * no per-frame settings reads: `enabled` is cached by the engine's
 * onSettingsChange subscription (rendering.dynamicGrading), and update()
 * writes the pipeline only while some grade is actually active, restoring
 * the authored base values exactly once when everything has decayed.
 */
class GradingController {
  constructor() {
    this.enabled = true;      // cached from settings by applyPipelineFlags
    this.suddenDeath = false;
    this.phase = 'round';
    this._sd = 0;             // sudden-death blend 0..1 (dt-eased, ~1.5s)
    this._lobby = 0;          // lobby blend 0..1
    this._winBoost = 0;       // round-win exposure impulse (decays ~2s)
    this._damageT = 0;        // damage-pulse clock, counts down from 0.4s
    this._active = false;     // whether we currently own the pipeline values
  }

  setSuddenDeath(on) { this.suddenDeath = !!on; }

  setPhase(phase) {
    if (phase === this.phase) return;
    // round -> lobby is the round-resolution moment: pulse warm.
    if (this.phase === 'round' && phase === 'lobby') this._winBoost = 0.1;
    this.phase = phase;
    if (phase === 'round') this.suddenDeath = false; // fresh round resets
  }

  /** Followed-bot hp drop: brief vignette squeeze (issue #184b). */
  damagePulse() { this._damageT = 0.4; }

  _reset(ip) {
    ip.exposure = 1.0;
    ip.contrast = 1.1;
    ip.vignetteWeight = 1.6;
    if (ip.vignetteColor) {
      ip.vignetteColor.r = 0;
      ip.vignetteColor.g = 0;
      ip.vignetteColor.b = 0.05;
    }
    this._active = false;
  }

  /** Per-frame from the render loop. Cheap: a few lerps, writes only while active. */
  update(pipeline, dt) {
    if (!pipeline || !pipeline.isSupported || !pipeline.imageProcessing) return;
    const ip = pipeline.imageProcessing;
    if (!this.enabled) {
      if (this._active) this._reset(ip);
      return;
    }

    const ease = 1 - Math.exp(-2 * dt); // ~95% converged in ~1.5s
    const sdTarget = this.suddenDeath && this.phase === 'round' ? 1 : 0;
    this._sd += (sdTarget - this._sd) * ease;
    this._lobby += ((this.phase === 'lobby' ? 1 : 0) - this._lobby) * ease;
    this._winBoost *= Math.exp(-2.3 * dt); // ~99% decayed in ~2s
    if (this._damageT > 0) this._damageT = Math.max(0, this._damageT - dt);
    const damage = this._damageT > 0 ? Math.sin(Math.PI * (this._damageT / 0.4)) : 0;

    if (this._sd < 0.002 && this._lobby < 0.002 && this._winBoost < 0.002 && damage === 0) {
      if (this._active) this._reset(ip);
      return;
    }
    this._active = true;
    ip.exposure = 1.0 - 0.05 * this._sd - 0.04 * this._lobby + this._winBoost;
    ip.contrast = 1.1 + 0.08 * this._sd - 0.05 * this._lobby;
    ip.vignetteWeight = 1.6 + 0.4 * this._sd + 0.5 * damage;
    if (ip.vignetteColor) {
      // Base (0,0,0.05) -> sudden-death red (0.25,0.02,0.04); the damage
      // pulse borrows the same red so both reads stay coherent.
      const red = Math.min(1, this._sd + damage * 0.8);
      ip.vignetteColor.r = 0.25 * red;
      ip.vignetteColor.g = 0.02 * red;
      ip.vignetteColor.b = 0.05 + (0.04 - 0.05) * red;
    }
  }
}

/**
 * Babylon's capability promise can remain pending on some GPU/driver paths.
 * Keep startup bounded so spectators transparently fall back to WebGL instead
 * of staring at an arena canvas that never initializes.
 */
export async function webGPUAvailableWithin(B, timeoutMs = WEBGPU_PROBE_TIMEOUT_MS) {
  const capability = B?.WebGPUEngine?.IsSupportedAsync;
  if (!capability || typeof capability.then !== 'function') return Boolean(capability);

  let timer = null;
  try {
    return Boolean(await Promise.race([
      Promise.resolve(capability),
      new Promise((resolve) => {
        timer = setTimeout(() => resolve(false), Math.max(0, timeoutMs));
      }),
    ]));
  } finally {
    if (timer !== null) clearTimeout(timer);
  }
}

/** A restarted Arena can reset its in-memory round counter to zero. */
export function roundStateReleasesTransition(stateRound, transitionRound) {
  const round = Number(stateRound);
  const heldRound = Number(transitionRound);
  return Number.isFinite(round) && Number.isFinite(heldRound) && round !== heldRound;
}

export class ArenaEngine {
  /** @param {HTMLCanvasElement} canvas @param {Object} opts */
  constructor(canvas, opts = {}) {
    this.canvas = canvas;
    this.arenaWidth = opts.arenaWidth || 2000;
    this.arenaHeight = opts.arenaHeight || 2000;
    this.engine = null;
    this.scene = null;
    this.camera = null;
    this.botRenderer = null;
    this.envRenderer = null;
    this.obstacleRenderer = null;
    this.pickupRenderer = null;
    this.effectRenderer = null;
    this.trailRenderer = null;
    this.projectileRenderer = null;
    this.gameplayRenderer = null;
    this.state = null;
    this.ready = false;
    this._seenArenaEvents = new Set();
    this._roundTransitionActive = false;
    this._roundTransitionRound = null;
    this._roundTransitionEntryTick = null;
    this._safeViewport = null;
  }

  /** Initialize Babylon engine. */
  async init() {
    const B = window.BABYLON;
    let engine;
    // WebGPU stays the preferred backend where the browser supports it: it is a
    // large CPU saving on this scene. `?webgpu=0` forces the WebGL path, which is
    // useful when triaging a client whose WebGPU driver misbehaves. The blank-arena
    // failure this file used to produce is handled where it actually happens, in the
    // render-loop guard below, rather than by giving up the WebGPU path for everyone.
    const forceWebGL = new URLSearchParams(location.search).get('webgpu') === '0';
    try {
      const webGPUSupported = !forceWebGL && await webGPUAvailableWithin(B);
      if (webGPUSupported) {
        engine = new B.WebGPUEngine(this.canvas, { antialias: false });
        await engine.initAsync();
        console.log('[Arena] WebGPU');
      } else {
        throw new Error(forceWebGL ? 'WebGL forced by ?webgpu=0' : 'WebGPU not supported');
      }
    } catch {
      // A WebGPUEngine attaches to the canvas in its CONSTRUCTOR, before
      // initAsync() has had the chance to fail. Dropping the reference here
      // strands that half-built engine holding a configured GPUCanvasContext
      // on this same canvas: it was never assigned to this.engine, so
      // dispose() cannot reach it and nothing else ever will.
      //
      // Measured live 2026-09-03: the default path left EngineStore.Instances
      // at 3 (two WebGPU, zero scenes, zero render loops, undisposed) against
      // exactly 1 under ?webgpu=0, so every failed WebGPU init leaked one.
      // Free it before putting a second engine on the same canvas.
      if (engine) {
        try {
          engine.dispose();
        } catch {
          /* half-built: free whatever exists and carry on */
        }
        engine = null;
      }
      engine = new B.Engine(this.canvas, false, {
        preserveDrawingBuffer: false,
        // PickupRenderer uses Babylon's HighlightLayer, whose WebGL path
        // requires an attached stencil buffer. Without it the layer logs a
        // warning on every startup and silently loses the pickup outline.
        stencil: true,
      });
      console.log('[Arena] WebGL');
    }
    // Cap at 1x device pixel ratio to prevent supersampling on HiDPI —
    // unless the user unchecks the resolutionCap setting, which lets capable
    // GPUs render at native resolution (up to 2x) for a sharper image.
    const applyResolution = () => {
      // Read the raw effect flag, NOT isEnabled(): this toggle has inverted
      // semantics (checked = cheaper). Turning the whole Rendering section
      // OFF means "less GPU work" and must not uncap resolution to 2x.
      const capFlag = getState().rendering?.effects?.resolutionCap !== false;
      const cap = capFlag ? 1 : 2;
      const level = 1.0 / Math.min(window.devicePixelRatio, cap);
      if (engine.getHardwareScalingLevel() !== level) {
        engine.setHardwareScalingLevel(level);
        engine.resize();
      }
    };
    applyResolution();
    // Post-process toggles (bloom/vignette/fxaa/sharpen) are applied here on
    // settings change — and once right after the pipeline is created below —
    // instead of re-reading isEnabled() every frame in the render loop.
    const applyPipelineFlags = () => {
      if (this.glowLayer) {
        this.glowLayer.isEnabled = isEnabled('rendering', 'glowLayer');
      }
      if (this._grading) {
        // Cached here so the grading tick in the render loop never reads
        // settings itself.
        this._grading.enabled = isEnabled('rendering', 'dynamicGrading');
      }
      if (!this.pipeline || !this.pipeline.isSupported) return;
      // Never re-enable a bloom pass that has already failed on this device.
      // This runs on every settings change AND once per init(), so without the
      // guard a scene rebuild silently undoes the containment below.
      this.pipeline.bloomEnabled = isEnabled('rendering', 'bloom') && !this._bloomBroken;
      this.pipeline.imageProcessing.vignetteEnabled = isEnabled('rendering', 'vignette');
      this.pipeline.fxaaEnabled = isEnabled('rendering', 'fxaa');
      this.pipeline.sharpenEnabled = isEnabled('rendering', 'sharpen');
    };
    // Depth fog (issue #183a): a denser navy EXP2 fog gives the far arena
    // edge a soft falloff while the default zoom stays nearly untouched
    // (visibility ~0.95 at radius 800, ~0.66 at the far corner from max
    // zoom-out). The skybox shader has no fog branch and the sky-distance
    // billboards + light shafts set applyFog=false, so only real arena
    // geometry participates. GUI overlays are a separate 2D layer.
    const applyDepthFog = () => {
      if (!this.scene) return;
      if (isEnabled('arenaAmbience', 'depthFog')) {
        this.scene.fogDensity = 0.00025;
        this.scene.fogColor.set(0.02, 0.04, 0.08);
      } else {
        this.scene.fogDensity = 0.00008;
        this.scene.fogColor.set(0.03, 0.03, 0.03);
      }
    };
    // World-identity toggles (issue #182) change round-built assets — floor
    // bake, obstacle merge, palette tints. Re-run those builds only when one
    // of the three flags actually flips; every other settings change is a
    // no-op here.
    const worldThemeSig = () =>
      `${isEnabled('arenaAmbience', 'mapPalettes')}|` +
      `${isEnabled('arenaAmbience', 'contactShadows')}|` +
      `${isEnabled('arenaAmbience', 'obstacleDetailing')}|` +
      `${isEnabled('arenaAmbience', 'smoothMapWalls')}`;
    this._worldThemeSig = worldThemeSig();
    const applyWorldTheme = () => {
      const sig = worldThemeSig();
      if (sig === this._worldThemeSig) return;
      this._worldThemeSig = sig;
      if (this.envRenderer) this.envRenderer.applyMapTheme();
      if (this.obstacleRenderer) this.obstacleRenderer.refresh();
    };
    // init() re-runs on between-round arena resizes; without unsubscribing in
    // dispose(), listeners would pile up holding disposed engines.
    this._unsubSettings = onSettingsChange(() => {
      applyResolution();
      applyPipelineFlags();
      applyDepthFog();
      applyWorldTheme();
    });
    this.engine = engine;
    // The steady-state form of the bloom bind fault does NOT throw. It arrives
    // as an uncaptured WebGPU validation error ("No bind group set at group
    // index 1" on the imageProcessing pass), which the render callback's catch
    // never sees, so containment cannot depend on the synchronous path alone.
    // GPUDevice is the only channel Babylon exposes for it; there is no public
    // observable. Guarded because the field is internal and absent on WebGL.
    try {
      const device = engine._device;
      if (device && typeof device.addEventListener === 'function') {
        device.addEventListener('uncapturederror', (ev) => {
          // Only act while the pass that is known to fail here is actually on.
          // A healthy device produces none of these, so a small run of them
          // with bloom enabled is the signal, not one-off noise.
          if (!this.pipeline || !this.pipeline.bloomEnabled) return;
          this._gpuErrors = (this._gpuErrors || 0) + 1;
          if (this._gpuErrors < 3) return;
          this._containBloomFailure(ev && ev.error ? ev.error : new Error('uncaptured WebGPU error'));
        });
      }
    } catch (e) { /* no device to watch; the synchronous path still applies */ }
    this._grading = new GradingController();
    const scene = new B.Scene(engine);
    this.scene = scene;
    scene.clearColor = new B.Color4(0, 0, 0.02, 1); // near-black to match starfield skybox
    scene.fogMode = B.Scene.FOGMODE_EXP2;
    scene.fogColor = new B.Color3(0.03, 0.03, 0.03);
    applyDepthFog(); // density + color come from the arenaAmbience.depthFog setting
    scene.skipPointerMovePicking = true;
    scene.autoClear = false;
    scene.autoClearDepthAndStencil = true;
    scene.blockMaterialDirtyMechanism = true;
    scene.useGeometryIdsMap = true;
    scene.useMaterialMeshMap = true;

    this.camera = new CameraController(scene, this.canvas, this.arenaWidth, this.arenaHeight);
    if (this._safeViewport) this.camera.setSafeViewport(this._safeViewport);
    this.envRenderer = new EnvironmentRenderer(scene, this.arenaWidth, this.arenaHeight);
    this.obstacleRenderer = new ObstacleRenderer(scene, this.envRenderer);
    this.botRenderer = new BotRenderer(scene);
    this.pickupRenderer = new PickupRenderer(scene);
    this.effectRenderer = new EffectRenderer(scene);
    this.effectRenderer.camera = this.camera;
    this.trailRenderer = new TrailRenderer(scene);
    this.projectileRenderer = new ProjectileRenderer(scene);
    this.gameplayRenderer = new GameplayRenderer(scene);
    // A stage resize can rebuild the scene during intermission. Preserve
    // round-transition ownership so the recreated renderer cannot briefly
    // revive the stale winner crown.
    if (this._roundTransitionActive) this.gameplayRenderer.beginRoundTransition();
    // Between-round spectator show (issue #189): driven by the server's
    // round_end broadcast through setState, per-frame from the render loop.
    // Created once and kept across mid-show stage resizes (issue #192):
    // resizeStageForShow detaches it around dispose() so a live show
    // survives the scene rebuild; the keyframe-driven _rebuildForArenaSize
    // path still disposes it (dispose() nulls the field) and gets a fresh
    // one here.
    this.intermissionDirector = this.intermissionDirector || new IntermissionDirector(this);
    this.gameplayRenderer.onStaffImpactCreated = (impact) => {
      // Same guard as the other effect spawns: projectile cleanup is
      // Animatable/render-loop-driven, which freezes without rendered frames
      // (hidden tab OR canvas scrolled off-screen).
      if (!this.shouldSpawnEffects()) return;
      const owner = (this.state?.bots || []).find((bot) => (bot.bot_id || bot.id) === impact.ownerId);
      if (!owner || !impact?.position) return;
      this.projectileRenderer.spawn(
        owner.position[0],
        owner.position[1],
        impact.position[0],
        impact.position[1],
        'staff',
        owner.avatar_color || '#8d4dff',
        undefined,
        { travelTime: Math.max(0.16, (impact.ticksLeft || 1) / 10) }
      );
    };
    this.botRenderer.onSelectionChange = (botId) => {
      if (this.onSelectBot) this.onSelectBot(botId);
    };
    scene.onPointerObservable.add((pointerInfo) => {
      const B = window.BABYLON;
      if (pointerInfo.type !== B.PointerEventTypes.POINTERDOWN) return;
      const pickedMesh = pointerInfo.pickInfo?.pickedMesh || null;
      if (!this.botRenderer.handlePick(pickedMesh)) {
        this.botRenderer.clearSelection();
      }
    });

    // Wire up attack → direct combat effects for non-event-driven weapons.
    // Effects are delayed to the CONTACT moment of the swing (opts.contactDelay,
    // computed from the weapon's windup+active phases) so sparks, the strike
    // read, and the victim's reaction land when the blow visually connects
    // instead of at swing start. setTimeout at event rate is fine; per-frame
    // work stays allocation-free.
    this.botRenderer.onAttack = (ax, az, tx, tz, color, weapon, opts) => {
      if (weapon === 'bow' || weapon === 'staff') {
        return;
      }
      const delayMs = Math.max(0, (opts?.contactDelay || 0) * 1000);
      const targetId = opts?.targetId || null;
      // Capture the scene live at schedule time. The between-round
      // _rebuildForArenaSize disposes and re-inits the scene, so a delayed
      // contact callback could otherwise fire against a disposed scene (final
      // teardown) or spawn a stale strike on the fresh scene (rebuild). Bail
      // unless the exact scene that owned this swing is still the live one.
      const swingScene = this.scene;
      setTimeout(() => {
        // No rendered frames (hidden tab or off-screen canvas) means no
        // render-loop cleanup, so strike meshes would pile up unseen.
        if (!this.effectRenderer || !this.shouldSpawnEffects() || this.scene !== swingScene || swingScene.isDisposed) return;
        this.effectRenderer.spawnWeaponStrike(ax, az, tx, tz, color, weapon);
        this.effectRenderer.spawnHitSparks(tx, tz, color, weapon);
        if (targetId && this.botRenderer) {
          const victim = this.botRenderer.entries.get(targetId);
          if (victim && victim.isAlive) {
            this.botRenderer.playImpactReaction(targetId, ax, az);
          }
        }
      }, delayMs);
    };

    // Wire up dodge → afterimage shimmer
    this.botRenderer.onDodge = (x, z, color) => {
      if (!this.shouldSpawnEffects()) return;
      this.effectRenderer.spawnDodgeEffect(x, z, color);
    };

    // Wire up shove → shockwave blast effect
    this.botRenderer.onShove = (ax, az, tx, tz, color) => {
      if (!this.shouldSpawnEffects()) return;
      this.effectRenderer.spawnShoveEffect(ax, az, tx, tz, color);
    };

    this._addLights();
    this.envRenderer.setupShadows(this.sunLight);

    // DefaultRenderingPipeline: stable FXAA + tone mapping, light sharpen only.
    const pipeline = new B.DefaultRenderingPipeline('defaultPipeline', true, this.scene, [this.camera.camera]);
    if (pipeline.isSupported) {
      pipeline.fxaaEnabled = true;
      pipeline.sharpenEnabled = true;
      pipeline.sharpen.edgeAmount = 0.15;
      pipeline.sharpen.colorAmount = 1.0;
      pipeline.imageProcessingEnabled = true;
      pipeline.imageProcessing.toneMappingEnabled = true;
      pipeline.imageProcessing.toneMappingType = B.ImageProcessingConfiguration.TONEMAPPING_ACES;
      pipeline.imageProcessing.exposure = 1.0;
      pipeline.imageProcessing.contrast = 1.1;
      // Bloom: the scene is built almost entirely from emissive materials and
      // additive particles (trims, rings, trails, explosions) but nothing glowed.
      // High threshold so only genuine highlights bloom; ACES keeps them controlled.
      // bloomScale 0.5 halves the post-pass cost for projector laptops.
      // _rebuildForArenaSize disposes the scene and calls init() again, so this
      // line runs once per arena resize. Starting it back at true is what made a
      // contained failure come straight back at the next round boundary.
      pipeline.bloomEnabled = !this._bloomBroken;
      pipeline.bloomThreshold = 0.75;
      pipeline.bloomWeight = 0.3;
      pipeline.bloomKernel = 48;
      pipeline.bloomScale = 0.5;
      // Subtle vignette frames the arena on a big screen.
      pipeline.imageProcessing.vignetteEnabled = true;
      pipeline.imageProcessing.vignetteWeight = 1.6;
      pipeline.imageProcessing.vignetteColor = new B.Color4(0, 0, 0.05, 0);
    }
    this.pipeline = pipeline;

    // GlowLayer (issue #181): real halos around the scene's emissive neon —
    // wall/obstacle trims, zone rings, weapon accents, trail cores. Half-res
    // main texture and a modest kernel keep it inside the projector-laptop
    // budget; intensity is tuned against the existing bloom (threshold 0.75,
    // weight 0.3, unchanged) so the two passes never stack into a blowout.
    this.glowLayer = null;
    if (typeof B.GlowLayer === 'function') {
      const glow = new B.GlowLayer('arenaGlow', scene, {
        mainTextureRatio: 0.5,
        blurKernelSize: 32,
      });
      glow.intensity = 0.75;
      for (const mesh of this.envRenderer.getGlowExcludedMeshes()) {
        glow.addExcludedMesh(mesh);
      }
      // Boundary walls (issue #186) are built later, at the first keyframe —
      // hand the layer over so their body mesh can be excluded on build
      // (only the wall trim should glow, like the perimeter walls above).
      this.obstacleRenderer.setGlowLayer(glow);
      // Zone rings are created lazily on the first zone update — the
      // environment excludes them at creation time (clip planes don't apply
      // in the glow pass).
      this.envRenderer.setGlowLayer(glow);
      this.glowLayer = glow;
    }

    // Apply persisted settings over the hardcoded creation defaults above so
    // a spectator who turned an effect off never sees a one-frame flash of it.
    applyPipelineFlags();

    const self = this;
    let _lastFrame = performance.now();
    let frameSuspended = false;
    const resetFrameClock = () => {
      _lastFrame = performance.now();
      if (document.hidden) frameSuspended = true;
    };
    this._visibilityHandler = resetFrameClock;
    document.addEventListener('visibilitychange', resetFrameClock);
    engine.runRenderLoop(() => {
      // Contain a throwing frame. Babylon zeroes _frameHandler before running this
      // callback and only re-queues the next frame AFTER it returns, with no
      // try/catch on that path, so ONE throw ends rendering permanently: the canvas
      // sits black at frameId 0 while spectator state keeps streaming in. Measured
      // live on a WebGPU client whose bloom pass failed to bind its textures.
      try {
      const now = performance.now();
      // Suspend the entire frame pipeline when no pixels can reach the
      // spectator. Reset the clock on every skipped callback so resuming
      // never inherits time spent hidden or off-screen.
      if (document.hidden || self._canvasVisible === false) {
        frameSuspended = true;
        _lastFrame = now;
        return;
      }
      if (frameSuspended) {
        if (self.botRenderer) self.botRenderer.resume();
        if (self.trailRenderer) {
          self.trailRenderer.reset(self.botRenderer ? self.botRenderer.entries : null);
        }
        frameSuspended = false;
      }
      const dt = Math.min((now - _lastFrame) / 1000, 0.1);
      _lastFrame = now;
      if (self.botRenderer && !self._roundTransitionActive) {
        self.botRenderer.interpolate();
      }
      if (self.trailRenderer) {
        self.trailRenderer.render(self.botRenderer ? self.botRenderer.entries : null, dt);
      }
      if (self.projectileRenderer) {
        self.projectileRenderer.update(dt);
      }
      if (self.gameplayRenderer) {
        self.gameplayRenderer.animate(self.botRenderer ? self.botRenderer.entries : null, dt);
      }
      if (self.intermissionDirector) {
        // Reads only settings flags cached via onSettingsChange — the loop
        // itself stays free of settings reads.
        self.intermissionDirector.update(dt);
      }
      if (self._grading) {
        // Enabled flag is cached by applyPipelineFlags; no settings reads here.
        self._grading.update(self.pipeline, dt);
      }
      // Pipeline toggles are event-driven (applyPipelineFlags via
      // onSettingsChange) — the per-frame loop stays free of settings reads.
      scene.render();
      } catch (err) {
        if (typeof self._onRenderLoopError === 'function') self._onRenderLoopError(err);
      }
    });
    // IntersectionObserver drives the off-screen frame suspension. threshold
    // 0 means any visible pixel keeps rendering.
    if (typeof IntersectionObserver === 'function' && !this._visObserver) {
      this._canvasVisible = true;
      this._visObserver = new IntersectionObserver((entries) => {
        for (const entry of entries) {
          // Some Chromium/WebGL combinations can transiently report
          // isIntersecting=false for a full-viewport canvas during layout.
          // Confirm the element is geometrically outside the viewport before
          // parking the render loop so one bad observer callback cannot freeze
          // a live arena. Truly off-screen/zero-size canvases still suspend.
          const rect = entry.boundingClientRect || this.canvas.getBoundingClientRect();
          const overlapsViewport = rect.width > 0 && rect.height > 0 &&
            rect.bottom > 0 && rect.right > 0 &&
            rect.top < window.innerHeight && rect.left < window.innerWidth;
          this._canvasVisible = entry.isIntersecting || overlapsViewport;
          if (!this._canvasVisible) frameSuspended = true;
          resetFrameClock();
        }
      }, { threshold: 0 });
      this._visObserver.observe(this.canvas);
    }
    this._resizeHandler = () => engine.resize();
    window.addEventListener('resize', this._resizeHandler);
    this.ready = true;
  }

  /**
   * Single containment entry point, reached from BOTH the synchronous render
   * callback catch and the asynchronous GPUDevice uncapturederror listener.
   * Latches rather than counts: the flag lives on the engine instance, which
   * outlives the scene, so the dispose and re-init that an arena resize
   * performs cannot undo it.
   * @private
   */
  _containBloomFailure(err) {
    if (this._bloomBroken) {
      if (this.pipeline && this.pipeline.bloomEnabled) this.pipeline.bloomEnabled = false;
      return;
    }
    this._bloomBroken = true;
    console.warn('[Arena] bloom failed on this device; keeping it off for the session', err);
    if (this.pipeline && this.pipeline.bloomEnabled) this.pipeline.bloomEnabled = false;
  }

  /**
   * Render-loop crash containment (blank-arena guard).
   *
   * Babylon does not guard the render callback: _processFrame zeroes
   * _frameHandler before running it and only re-queues the next frame after it
   * returns, so a single throw ends rendering permanently. The canvas then sits
   * black at frameId 0 while the spectator socket keeps streaming state, which
   * reads to a viewer as "the arena is down" with nothing in the log after the
   * first error.
   *
   * First failure: drop the effect most likely to be at fault (the bloom pass,
   * whose bind-group failure is what was observed on a WebGPU client) and keep
   * rendering. Repeated failures: turn the whole post-process pipeline off
   * rather than spin on a broken frame. The scene itself keeps drawing either
   * way, which is what the spectator actually came for.
   * @private
   */
  _onRenderLoopError(err) {
    this._renderFailures = (this._renderFailures || 0) + 1;
    if (this._renderFailures === 1) {
      console.error('[Arena] render loop threw; containing so the canvas keeps drawing', err);
      // A contained failure still means the frame is degraded. Report it: the
      // containment is what keeps the arena visible, not a reason to go quiet.
      // Reported through a global hook rather than an import: the reporter is
      // only ever needed on this rare path, and putting it in the renderer's
      // module graph measurably delayed first frame (~9% on a software
      // renderer). Optional call also keeps containment working when the hook
      // is absent, including where this method is evaluated in isolation
      // (scripts/test-bloom-failure-latch.mjs).
      globalThis.__arenaReportError?.('render-loop', err, { source: 'engine.runRenderLoop' });
    }
    // Latch, do not count. The failure this contains is a WebGPU bind-group
    // fault in the bloom merge pass, and after the pass is dropped the same
    // fault resurfaces as an ASYNC GPUValidationError that never throws into
    // JS, so this handler is not called a second time. Anything gated on a
    // later failure count therefore never runs. The flag lives on the engine
    // instance, which outlives the scene, so it also survives the dispose and
    // re-init that a between-round arena resize performs.
    this._containBloomFailure(err);
    if (this._renderFailures >= 6 && this.pipeline) {
      console.warn('[Arena] repeated render-loop failures; disabling the post-process pipeline');
      try { this.pipeline.dispose(); } catch (e) { /* already gone */ }
      this.pipeline = null;
    }
  }

  /** @private */
  _addLights() {
    const B = window.BABYLON;
    const dir = new B.DirectionalLight('sun', new B.Vector3(-0.4, -1, 0.3), this.scene);
    dir.position = new B.Vector3(0, 80, -40);
    dir.intensity = 0.82;
    dir.diffuse = new B.Color3(1, 0.95, 0.85);
    dir.specular = new B.Color3(0.34, 0.34, 0.34);
    this.sunLight = dir;

    const hemi = new B.HemisphericLight('hemi', new B.Vector3(0, 1, 0), this.scene);
    hemi.intensity = 0.46;
    hemi.diffuse = new B.Color3(0.66, 0.72, 0.88);
    hemi.specular = B.Color3.Black();
    hemi.groundColor = new B.Color3(0.09, 0.1, 0.12);
  }

  /**
   * Feed arena state from spectator WS.
   * @param {Object} state
   */
  setState(state) {
    if (!this.ready) return;
    // Both spectator shells (app.js and m/mobile.js) feed every broadcast
    // through here, so the grading controller learns the round phase without
    // extra app-layer wiring; setGamePhase/setSuddenDeath below stay public
    // for anything that wants to drive it directly.
    if (state.type === 'lobby_state') {
      this.setGamePhase('lobby');
      // The lobby countdown tells the intermission show when the next round
      // actually starts, so the construction can pace itself to finish then.
      if (this.intermissionDirector) this.intermissionDirector.handleLobbyState(state);
      return;
    }
    if (state.type === 'round_end') {
      // Typed spectator round_end (issue #189): starts the intermission
      // show. Old servers never send it, so the feature stays inert there.
      this._beginRoundTransition(state);
      if (this.intermissionDirector) this.intermissionDirector.handleRoundEnd(state);
      return;
    }
    if (state.type !== 'arena_state') return;
    // The first arena_state of the NEXT round snap-completes a running
    // intermission show — before the resize check below so a dynamic arena
    // rebuild never tears the scene down under live show artifacts.
    if (this.intermissionDirector) this.intermissionDirector.handleArenaState(state);
    this._maybeEndRoundTransition(state);
    // Keep this guard for cached/older servers and reordered frames: while the
    // ended round still owns the transition, its stale combat events and HP
    // deltas must not spawn effects around already-despawned bots.
    if (this._roundTransitionActive) return;
    this.setGamePhase('round');
    this.setSuddenDeath(!!state.sudden_death);

    // Dynamic arena sizing: the map can change dimensions between rounds
    // (it grows with bot count). Keyframe states carry arena_size; when it
    // differs from the scene we built, rebuild the whole scene at the new
    // size. This only ever happens at round boundaries.
    const size = state.arena_size;
    if (size && size.length === 2 && !this._resizing &&
        (size[0] !== this.arenaWidth || size[1] !== this.arenaHeight)) {
      this._rebuildForArenaSize(size[0], size[1], state);
      return;
    }

    this.state = state;
    // Transient combat effects only spawn while frames actually render.
    // Chrome parks rAF for hidden/occluded windows, and the render loop now
    // also skips while the canvas is scrolled off-screen — WS states keep
    // arriving at 10Hz either way, and every effect's cleanup runs in the
    // render loop, so spawning here would grow the scene without bound.
    // _seenArenaEvents dedup means skipped events never replay.
    // Map shape first (issue #182): a round-boundary keyframe carries the new
    // shape and the new obstacle layout together, so theming the environment
    // before the obstacle rebuild lets the merged meshes pick up the round's
    // palette in one pass.
    this.envRenderer.setMapShape(state.map_shape);
    // Followed-bot damage pulse (issue #184b): when the spectator is locked
    // onto a bot and its hp drops, squeeze the vignette briefly via the
    // grading controller (composes additively with sudden-death grading).
    // One-shot trigger, so the isEnabled gate sits here at the spawn point.
    const followId = this.camera ? this.camera.followId : null;
    if (followId) {
      const followed = (state.bots || []).find((b) => b.bot_id === followId);
      const hp = followed ? followed.hp : null;
      if (this._grading && followId === this._followedBotId &&
          hp != null && this._followedBotHp != null && hp < this._followedBotHp &&
          isEnabled('hitReactions', 'damageVignette')) {
        this._grading.damagePulse();
      }
      this._followedBotId = followId;
      this._followedBotHp = hp;
    } else {
      this._followedBotId = null;
      this._followedBotHp = null;
    }
    // While the intermission show is live, stale intermission broadcasts
    // still describe the ENDED round: keyframes would rebuild the map the
    // teardown just sank, and bot snapshots would re-create the entries the
    // despawn removed. The director releases both holds on fast-forward
    // (handleArenaState above), so the new round's first state — including
    // the one that triggers the fast-forward — always flows through.
    const showHoldsWorld = this.intermissionDirector && this.intermissionDirector.holdsWorld();
    const showHoldsBots = this.intermissionDirector && this.intermissionDirector.holdsBots();
    if (!showHoldsWorld) this.obstacleRenderer.update(state.obstacles, state.mask_rects);
    this.envRenderer.update(state.safe_zone, !!state.sudden_death);
    if (!showHoldsBots) this.botRenderer.update(state.bots);
    // Events play after the entity updates so a taunt arriving in the same
    // broadcast that introduces its bot can find the fresh entry.
    if (this.shouldSpawnEffects()) {
      this._playArenaEvents(state.events || [], state);
    }
    this.pickupRenderer.update(state.pickups || []);
    this.effectRenderer.update(state.bots);
    this.gameplayRenderer.update(state);
    this.camera.updateBotPositions(state.bots);
  }

  /** @private Transfer bot/gameplay animation ownership to the round show. */
  _beginRoundTransition(state) {
    const round = Number(state && (state.round_number ?? state.round));
    const currentRound = Number(this.state && this.state.round_number);
    this._roundTransitionRound = Number.isFinite(round)
      ? round
      : (Number.isFinite(currentRound) ? currentRound : 0);
    // Remember where the ENDING round's clock was. arena_state carries round_tick
    // (ticks since the round began) and it restarts at the next round, so a later
    // round_tick BELOW this one is an unambiguous "the next round is running"
    // signal that does not depend on a round number the payload never carries.
    const entryTick = Number(this.state && this.state.round_tick);
    this._roundTransitionEntryTick = Number.isFinite(entryTick) ? entryTick : null;
    this._roundTransitionActive = true;
    if (this.gameplayRenderer) this.gameplayRenderer.beginRoundTransition();
  }

  /** @private Resume only for a different authoritative round. The counter
   * can reset after a server restart, so strict numeric increase is unsafe. */
  _maybeEndRoundTransition(state) {
    if (!this._roundTransitionActive) return;
    // Read the round the SAME way the enter path does (line above uses
    // round_number ?? round). Reading only round_number here made entering and
    // releasing asymmetric: a payload carrying `round` enters the transition
    // fine, then never releases, because Number(undefined) is NaN and
    // roundStateReleasesTransition requires both values finite. The map teardown
    // that starts the transition is then permanent, which renders as a black
    // arena with only the emissive glow left while the HUD and minimap keep
    // updating normally.
    const releaseRound = state && (state.round_number ?? state.round);
    let release = roundStateReleasesTransition(releaseRound, this._roundTransitionRound);
    if (!release) {
      // The round-number rule alone is unreachable here: the transition is entered
      // from round_end, but the only payload that reaches this method is
      // arena_state, whose server-side view (SpectatorState) carries no round
      // number at all. Both reads are therefore undefined, the predicate needs two
      // finite values, and the transition never lifts, leaving the map teardown
      // permanent: a black arena showing only emissive meshes while the HUD, kill
      // feed and minimap keep updating from the very same payloads.
      const tick = Number(state && state.round_tick);
      const entry = this._roundTransitionEntryTick;
      release = Number.isFinite(tick) && Number.isFinite(entry) && tick < entry;
    }
    if (!release) return;
    this._roundTransitionActive = false;
    this._roundTransitionRound = null;
    this._roundTransitionEntryTick = null;
    if (this.gameplayRenderer) this.gameplayRenderer.endRoundTransition();
  }

  /**
   * @private Tear down and rebuild the whole scene at new arena dimensions.
   * Every renderer has a dispose path and module-level caches are
   * scene-aware, so a full rebuild is safe; external wiring (controls, HUD
   * callbacks) stays valid because the ArenaEngine instance survives.
   */
  async _rebuildForArenaSize(w, h, state) {
    this._resizing = true;
    console.log(`[Arena] arena size changed to ${w}x${h} — rebuilding scene`);
    const prevFollow = this.camera ? this.camera.followId : null;
    const prevZoom = this.camera ? this.camera.zoom : null;
    // app.js assigns onZoomChange to the controller instance, and init()
    // replaces that instance — carry the callback over or the zoom slider
    // silently stops syncing after the first between-round arena resize.
    const prevOnZoomChange = this.camera ? this.camera.onZoomChange : null;
    this.ready = false;
    try {
      this.dispose();
      this.arenaWidth = w;
      this.arenaHeight = h;
      this.state = null;
      await this.init();
      if (prevOnZoomChange && this.camera) this.camera.onZoomChange = prevOnZoomChange;
      if (prevZoom) this.setZoom(prevZoom);
      if (prevFollow) this.followBot(prevFollow);
    } catch (err) {
      console.error('[Arena] scene rebuild failed:', err);
    } finally {
      this._resizing = false;
    }
    // Apply the keyframe that triggered the rebuild so the new scene
    // populates immediately instead of waiting for the next broadcast.
    if (this.ready && state) this.setState(state);
  }

  /**
   * Mid-show arena resize (issue #192): the intermission director calls this
   * between teardown and construction when the next round changes the arena
   * dimensions, so the stage rebuild happens INSIDE the show instead of the
   * construction phase being skipped. Same dispose/init cycle as
   * _rebuildForArenaSize, but the director is detached around dispose() so
   * the live show survives (init() reuses an existing director), and no
   * keyframe replay happens — the show still owns the world until its
   * handoff or fast-forward.
   * @returns {Promise<boolean>|boolean} resolves true when the stage is
   *   rebuilt at the requested size.
   */
  async resizeStageForShow(w, h) {
    if (!this.ready || this._resizing) return false;
    if (w === this.arenaWidth && h === this.arenaHeight) return true;
    this._resizing = true;
    console.log(`[Arena] intermission stage resize to ${w}x${h}`);
    const director = this.intermissionDirector;
    const prevFollow = this.camera ? this.camera.followId : null;
    const prevZoom = this.camera ? this.camera.zoom : null;
    const prevOnZoomChange = this.camera ? this.camera.onZoomChange : null;
    this.ready = false;
    try {
      this.intermissionDirector = null; // detach: the show must outlive the scene
      this.dispose();
      this.intermissionDirector = director;
      this.arenaWidth = w;
      this.arenaHeight = h;
      this.state = null;
      await this.init();
      if (prevOnZoomChange && this.camera) this.camera.onZoomChange = prevOnZoomChange;
      if (prevZoom) this.setZoom(prevZoom);
      if (prevFollow) this.followBot(prevFollow);
      return true;
    } catch (err) {
      console.error('[Arena] intermission stage resize failed:', err);
      return false;
    } finally {
      this._resizing = false;
    }
  }

  _playArenaEvents(events, state) {
    for (const ev of events) {
      if (!ev || !ev.id || this._seenArenaEvents.has(ev.id)) continue;
      this._seenArenaEvents.add(ev.id);

      if (this._seenArenaEvents.size > 256) {
        const first = this._seenArenaEvents.values().next();
        if (!first.done) this._seenArenaEvents.delete(first.value);
      }

      if (ev.type === 'teleport' && ev.from_position && ev.to_position) {
        this.effectRenderer.spawnTeleportBurst(
          ev.from_position[0], ev.from_position[1],
          ev.to_position[0], ev.to_position[1],
          ev.color || '#00ffff'
        );
      } else if (ev.type === 'bow_fired' && ev.from_position && ev.to_position) {
        this.projectileRenderer.spawn(
          ev.from_position[0], ev.from_position[1],
          ev.to_position[0], ev.to_position[1],
          'bow',
          ev.color || '#f0e6c9',
          undefined,
          { intensity: ev.intensity || 1 },
        );
      } else if (ev.type === 'bow_impact' && ev.position) {
        this.effectRenderer.spawnBowImpact(
          ev.position[0], ev.position[1],
          ev.color || '#f0e6c9',
          !!ev.target_id,
          ev.intensity || 1
        );
        if (ev.target_id && this.botRenderer) {
          this.botRenderer.playImpactReaction(ev.target_id);
        }
      } else if (ev.type === 'spear_brace' && ev.from_position && ev.position) {
        this.effectRenderer.spawnSpearBrace(
          ev.from_position[0], ev.from_position[1],
          ev.position[0], ev.position[1],
          ev.color || '#ffe38a'
        );
        if (ev.target_id && this.botRenderer) {
          this.botRenderer.playImpactReaction(ev.target_id);
        }
      } else if (ev.type === 'shield_bash' && ev.from_position && ev.position) {
        this.effectRenderer.spawnShieldBash(
          ev.from_position[0], ev.from_position[1],
          ev.position[0], ev.position[1],
          ev.color || '#bfe3ff'
        );
        if (ev.target_id && this.botRenderer) {
          this.botRenderer.playImpactReaction(ev.target_id);
        }
      } else if (ev.type === 'backstab' && ev.from_position && ev.position) {
        this.effectRenderer.spawnBackstab(
          ev.from_position[0], ev.from_position[1],
          ev.position[0], ev.position[1],
          ev.color || '#ff8f47'
        );
        if (ev.target_id && this.botRenderer) {
          this.botRenderer.playImpactReaction(ev.target_id);
        }
      } else if ((ev.type === 'grapple_pull' || ev.type === 'grapple_anchor') && ev.from_position && ev.to_position) {
        const owner = (state?.bots || []).find((b) => b.bot_id === ev.owner_id || b.id === ev.owner_id);
        const anchor = ev.position || ev.to_position;
        if (ev.type === 'grapple_pull' && owner) {
          this.effectRenderer.spawnGrappleEffect(
            owner.position[0], owner.position[1],
            ev.from_position[0], ev.from_position[1],
            { mode: 'pull', endX: ev.to_position[0], endZ: ev.to_position[1], color: ev.color || '#59f1ff' }
          );
        } else {
          this.effectRenderer.spawnGrappleEffect(
            ev.from_position[0], ev.from_position[1],
            anchor[0], anchor[1],
            { mode: 'anchor', endX: ev.to_position[0], endZ: ev.to_position[1], color: ev.color || '#59f1ff' }
          );
        }
      } else if (ev.type === 'grapple_slam' && ev.from_position && ev.position) {
        this.effectRenderer.spawnGrappleSlam(
          ev.from_position[0], ev.from_position[1],
          ev.position[0], ev.position[1],
          ev.color || '#59f1ff'
        );
        if (ev.target_id && this.botRenderer) {
          this.botRenderer.playImpactReaction(ev.target_id);
        }
      } else if (ev.type === 'taunt' && ev.owner_id && ev.text) {
        if (this.botRenderer) this.botRenderer.showTaunt(ev.owner_id, ev.text);
      } else if (ev.type === 'flag_captured' && ev.position) {
        // CTF capture: celebratory burst at the base.
        this.effectRenderer.spawnMineExplosion(ev.position[0], ev.position[1], 30);
        this.effectRenderer.spawnHitSparks(ev.position[0], ev.position[1], '#ffd700', 'sword');
      } else if ((ev.type === 'flag_taken' || ev.type === 'flag_returned' || ev.type === 'flag_dropped') && ev.position) {
        this.effectRenderer.spawnHitSparks(
          ev.position[0], ev.position[1],
          ev.type === 'flag_taken' ? '#ff5a4d' : '#7ef7ff',
          'sword'
        );
      } else if (ev.type === 'mine_detonated' && ev.position) {
        this.effectRenderer.spawnMineExplosion(
          ev.position[0], ev.position[1],
          (ev.radius || 1) * 20
        );
      } else if (ev.type === 'staff_detonated' && ev.position) {
        this.effectRenderer.spawnStaffExplosion(
          ev.position[0], ev.position[1],
          (ev.radius || 1) * 20,
          ev.color || '#8d4dff'
        );
      } else if (ev.type === 'capture_pad_captured' && ev.position) {
        this.effectRenderer.spawnCapturePadPulse(
          ev.position[0], ev.position[1],
          (ev.radius || 2) * 20,
          ev.color || '#7ef7ff'
        );
      }
    }
  }

  /**
   * Whether transient combat effects may spawn right now. Effects clean
   * themselves up from the render loop, so they must only spawn while frames
   * are actually rendering: not in a hidden tab (rAF parked) and not while
   * the canvas is scrolled off-screen (render loop skipped). The `!== false`
   * form keeps behavior identical when IntersectionObserver is unavailable.
   */
  shouldSpawnEffects() { return !document.hidden && this._canvasVisible !== false; }

  /** Dynamic-grading entry points (issue #183c). Phases: 'round' | 'lobby'. */
  setGamePhase(phase) { if (this._grading) this._grading.setPhase(phase); }
  setSuddenDeath(on) { if (this._grading) this._grading.setSuddenDeath(on); }

  setZoom(z) { if (this.camera) this.camera.setZoom(z); }
  followBot(id) { if (this.camera) this.camera.followBot(id); }
  setAutoPan(on) { if (this.camera) this.camera.setAutoPan(on); }
  setSafeViewport(viewport) {
    this._safeViewport = viewport || null;
    if (this.camera) this.camera.setSafeViewport(this._safeViewport);
  }
  getState() { return this.state; }
  selectBot(id) { if (this.botRenderer) this.botRenderer.selectBot(id); }

  /**
   * Return a serializable renderer snapshot for browser diagnostics. This is
   * intentionally a method instead of exposing Babylon objects: smoke tests
   * and support tooling can compare lifecycle baselines without taking
   * ownership of scene resources or depending on renderer implementation
   * details.
   */
  getLifecycleSnapshot() {
    const scene = this.scene;
    const activeParticles = scene?.getActiveParticles ? scene.getActiveParticles() : null;
    const activeParticleCount = Number.isFinite(Number(activeParticles))
      ? Number(activeParticles)
      : (Number(activeParticles?.length) || 0);
    const roundNumber = Number(this.state?.round_number);
    const resources = {
      meshes: scene?.meshes?.length || 0,
      materials: scene?.materials?.length || 0,
      textures: scene?.textures?.length || 0,
      particleSystems: scene?.particleSystems?.length || 0,
      transformNodes: scene?.transformNodes?.length || 0,
      activeParticles: activeParticleCount,
    };
    const bots = [];
    if (this.botRenderer?.entries) {
      for (const [id, entry] of this.botRenderer.entries) {
        bots.push({
          id,
          x: Number(entry?.root?.position?.x) || 0,
          y: Number(entry?.root?.position?.y) || 0,
          z: Number(entry?.root?.position?.z) || 0,
          enabled: typeof entry?.root?.isEnabled === 'function' ? entry.root.isEnabled() : false,
          labelVisible: entry?.worldHud?.nameLabel?.isVisible !== false,
        });
      }
    }
    bots.sort((left, right) => String(left.id).localeCompare(String(right.id)));
    const bounty = this.gameplayRenderer?.bountyGroup;
    const ring = bounty?.ring;
    return {
      ready: this.ready,
      roundNumber: Number.isFinite(roundNumber) ? roundNumber : null,
      roundTransitionActive: this._roundTransitionActive,
      intermissionActive: this.intermissionDirector?.active === true,
      arenaSize: [this.arenaWidth, this.arenaHeight],
      safeViewport: this._safeViewport ? { ...this._safeViewport } : null,
      resources,
      bots,
      bounty: {
        targetId: this.gameplayRenderer?.bountyTargetId || null,
        visible: !!(ring && !ring.isDisposed?.() && ring.visibility > 0 && ring.isEnabled()),
        emitRate: Number(bounty?.sparkle?.emitRate) || 0,
      },
    };
  }

  dispose() {
    if (this._resizeHandler) {
      window.removeEventListener('resize', this._resizeHandler);
    }
    if (this._visibilityHandler) {
      document.removeEventListener('visibilitychange', this._visibilityHandler);
      this._visibilityHandler = null;
    }
    if (this._unsubSettings) {
      this._unsubSettings();
      this._unsubSettings = null;
    }
    if (this._visObserver) {
      this._visObserver.disconnect();
      this._visObserver = null;
    }
    if (this.intermissionDirector) {
      this.intermissionDirector.dispose();
      this.intermissionDirector = null;
    }
    if (this.camera && this.camera.dispose) this.camera.dispose();
    if (this.projectileRenderer) this.projectileRenderer.dispose();
    if (this.trailRenderer) this.trailRenderer.dispose();
    if (this.effectRenderer && this.effectRenderer.dispose) this.effectRenderer.dispose();
    if (this.envRenderer && this.envRenderer.dispose) this.envRenderer.dispose();
    if (this.engine) {
      this.engine.stopRenderLoop();
      this.scene.dispose();
      this.engine.dispose();
    }
  }
}
