import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');
const isolatedSource = source.replace(/import[\s\S]*?from '[^']+';\r?\n/g, '');
const moduleURL = `data:text/javascript;base64,${Buffer.from(isolatedSource).toString('base64')}`;
const { webGPUAvailableWithin, ArenaEngine, replaceCanvasElement } = await import(moduleURL);

assert.match(
  source,
  /new B\.Engine\(this\.canvas, false, \{[\s\S]{0,420}stencil:\s*true/,
  'the WebGL fallback must enable stencil for the pickup HighlightLayer',
);

assert.equal(typeof webGPUAvailableWithin, 'function',
  'the renderer must expose one bounded WebGPU capability probe');

const neverSettles = new Promise(() => {});
const startedAt = performance.now();
const timedOut = await webGPUAvailableWithin({
  WebGPUEngine: { IsSupportedAsync: neverSettles },
}, 10);
const elapsed = performance.now() - startedAt;

assert.equal(timedOut, false, 'an unresolved WebGPU probe must fall back to WebGL');
assert.ok(elapsed < 250, `WebGPU fallback exceeded its bounded test window (${elapsed.toFixed(1)}ms)`);
assert.equal(await webGPUAvailableWithin({
  WebGPUEngine: { IsSupportedAsync: Promise.resolve(true) },
}, 100), true);

console.log('WebGPU capability probing is bounded and falls back to WebGL when the browser stalls');

// Regression gate for the blank-arena fix (#228).
//
// Babylon zeroes _frameHandler before running the render callback and only
// re-queues the next frame AFTER it returns, with no try/catch on that path, so a
// single throw ends rendering forever: activeRenderLoops stays 1, _frameHandler
// stays 0, frameId never leaves 0, and the canvas is black while state streams in.
// That exact state was measured on a live WebGPU client. The fix is containment in
// the loop, so assert it is there and cannot be quietly removed.
const loopStart = source.indexOf('engine.runRenderLoop(() => {');
const loopEnd = source.indexOf("if (typeof IntersectionObserver === 'function'", loopStart);
assert.ok(loopStart >= 0 && loopEnd > loopStart, 'the render loop must stay discoverable');
const loopSrc = source.slice(loopStart, loopEnd);
assert.match(loopSrc, /try \{/, 'the render callback must open a try block');
assert.match(loopSrc, /scene\.render\(\);/, 'the guarded body must still render the scene');
assert.match(loopSrc, /catch \(err\)[\s\S]{0,160}_onRenderLoopError/,
  'a throwing frame must be routed to the handler, or one bad frame kills rendering permanently');
assert.match(source, /_onRenderLoopError\(err\) \{/,
  'engine.js must define the render-loop error handler');
assert.match(source, /bloomEnabled = false/,
  'the first contained failure must drop the bloom pass rather than the whole renderer');

// WebGPU stays the default where supported (it is a large CPU saving); ?webgpu=0 is
// the triage escape hatch. Assert the predicate really is an exact "0" match, so a
// stray ?webgpu or ?webgpu=false cannot silently force everyone onto WebGL.
const gateLine = source.match(/const forceWebGL = .*;/);
assert.ok(gateLine, 'engine.js must expose an explicit forceWebGL predicate');
const evalGate = (search) => new Function('location', `${gateLine[0]} return forceWebGL;`)({ search });
assert.equal(evalGate('?webgpu=0'), true, '?webgpu=0 must force WebGL');
for (const search of ['', '?webgpu', '?webgpu=1', '?webgpu=false', '?other=0']) {
  assert.equal(evalGate(search), false, `WebGPU must remain preferred for ${search || '(no query)'}`);
}

console.log('render-loop failures are contained, and WebGPU stays default with a ?webgpu=0 escape hatch');

// Regression gate for the leaked-engine blank arena, measured live 2026-09-03.
//
// B.WebGPUEngine attaches to the canvas in its CONSTRUCTOR, before initAsync()
// can fail. If the catch replaces the local `engine` without disposing it, that
// half-built engine keeps a configured GPUCanvasContext on the SAME canvas for
// the life of the page: it was never assigned to this.engine, so dispose()
// cannot reach it and nothing else ever will. On a client whose WebGPU init
// fails, the canvas then presents a dead context while a live WebGL engine
// renders into nothing visible, which is the blank arena with a working HUD.
//
// Measured on https://arena.angel-serv.com: the default path left
// EngineStore.Instances at 3 (two WebGPU, zero scenes, zero render loops, all
// undisposed, all bound to #arena-canvas) against exactly 1 under ?webgpu=0.
const initAsyncAt = source.indexOf('await engine.initAsync();');
assert.ok(initAsyncAt >= 0, 'the WebGPU init await must stay discoverable');
const fallbackCatchAt = source.indexOf('} catch {', initAsyncAt);
const webglEngineAt = source.indexOf('new B.Engine(this.canvas, false, {', fallbackCatchAt);
assert.ok(fallbackCatchAt > initAsyncAt && webglEngineAt > fallbackCatchAt,
  'the WebGPU-to-WebGL fallback must stay discoverable');
const fallbackSrc = source.slice(fallbackCatchAt, webglEngineAt);
assert.match(fallbackSrc, /engine\.dispose\(\)/,
  'the failed WebGPU engine must be disposed before a second engine is attached to the same canvas');

console.log('a failed WebGPU init disposes its half-built engine instead of stranding it on the canvas');

// Regression gate for the OTHER half of that blank arena, and the half the
// dispose above does not cover.
//
// A canvas element keeps its rendering context for life: the first getContext()
// to succeed fixes the type, any later call for a different type returns null,
// and nothing gives the element back. B.WebGPUEngine asks for 'webgpu' in its
// CONSTRUCTOR, before initAsync() can fail, so by the time the fallback runs the
// canvas is a WebGPU canvas permanently. Disposing frees the device and leaves
// the element claimed and presenting nothing, while a perfectly healthy WebGL
// engine renders into a context the compositor never reads — the blank arena
// with a working HUD that survived the dispose fix.
//
// This drives the real ArenaEngine.init() against a canvas that enforces the
// spec's one-context-per-element rule, rather than matching on source text: the
// question is which element the WebGL engine is handed, and only running it
// answers that.
{
  let nextId = 0;
  const makeCanvas = () => ({
    _id: nextId++,
    _context: null,
    parentNode: null,
    getContext(type) {
      if (this._context && this._context !== type) return null;
      this._context = type;
      return { type };
    },
    cloneNode() { return makeCanvas(); },
    replaceWith(next) {
      const parent = this.parentNode;
      if (!parent) return;
      parent.children[parent.children.indexOf(this)] = next;
      next.parentNode = parent;
      this.parentNode = null;
    },
    getBoundingClientRect: () => ({ width: 800, height: 600 }),
  });

  const container = { children: [] };
  const canvas = makeCanvas();
  container.children.push(canvas);
  canvas.parentNode = container;

  let webgpuCanvas = null;
  let webglCanvas = null;
  const previousWindow = globalThis.window;
  const previousLocation = globalThis.location;
  globalThis.location = { search: '' };
  globalThis.window = {
    devicePixelRatio: 1,
    BABYLON: {
      WebGPUEngine: class {
        static IsSupportedAsync = Promise.resolve(true);
        constructor(c) { webgpuCanvas = c; c.getContext('webgpu'); }
        async initAsync() { throw new Error('WebGPU device request failed'); }
        dispose() {}
      },
      Engine: class {
        constructor(c) { webglCanvas = c; this.context = c.getContext('webgl2'); }
        getHardwareScalingLevel() { return 1; }
        setHardwareScalingLevel() {}
        resize() {}
      },
    },
  };
  try {
    // init() carries on into scene construction this harness deliberately does
    // not stub. Both engines have been built by then, which is the whole
    // question here, so the throw past that point is expected and ignored.
    await new ArenaEngine(canvas, {}).init();
  } catch { /* scene setup is out of scope */ } finally {
    globalThis.window = previousWindow;
    globalThis.location = previousLocation;
  }

  assert.ok(webgpuCanvas, 'the WebGPU engine must have been constructed');
  assert.ok(webglCanvas, 'the WebGL fallback must have been constructed');
  assert.notEqual(webglCanvas, webgpuCanvas,
    'the WebGL fallback was handed the canvas the WebGPU engine already claimed: '
    + 'that element can never return a WebGL context again, so it presents nothing');
  assert.equal(webglCanvas._context, 'webgl2',
    'the fallback canvas must actually have answered a WebGL context request');
  assert.equal(webgpuCanvas._context, 'webgpu',
    'the harness must really model the element the WebGPU engine claimed');
  assert.equal(container.children.length, 1,
    'the replacement canvas must take the old one\'s place, not sit beside it');
  assert.equal(container.children[0], webglCanvas,
    'the element left in the document must be the one the live engine renders into');
}

// The swap is only correct because it is confined to the branch where a
// WebGPUEngine was really constructed. A ?webgpu=0 or unsupported start never
// touches the canvas, so replacing it there would throw away an element that
// was fine — and detach one the caller still holds.
assert.match(
  fallbackSrc,
  /if \(engine\) \{[\s\S]*replaceCanvasElement\(this\.canvas\)[\s\S]*\}/,
  'the canvas swap must sit inside the "a WebGPU engine was built" branch',
);
assert.equal(typeof replaceCanvasElement, 'function',
  'engine.js must expose the canvas swap for this gate to exercise');
{
  const detached = { cloneNode: () => ({}), parentNode: null };
  assert.equal(replaceCanvasElement(detached), detached,
    'a detached canvas has nothing to swap into and must be returned unchanged');
  assert.equal(replaceCanvasElement(null), null, 'no canvas is not an error');
}

// Regression gate for the between-round stall.
//
// init() runs again on every scene rebuild (_rebuildForArenaSize,
// resizeStageForShow) and the ArenaEngine instance survives those, so on a
// client where WebGPU does not work the old code paid, once per round
// boundary: the capability probe, a device request that fails on its own
// schedule, a canvas swap, and a from-scratch WebGL context whose shaders all
// recompile. Several seconds of frozen page, every round, forever.
//
// The answer cannot change within a page, so the first failure is remembered.
// Asserted by counting what the SECOND init() actually does.
{
  let nextId = 0;
  const makeCanvas = () => ({
    _id: nextId++,
    _context: null,
    parentNode: null,
    getContext(type) {
      if (this._context && this._context !== type) return null;
      this._context = type;
      return { type };
    },
    cloneNode() { return makeCanvas(); },
    replaceWith(next) {
      const parent = this.parentNode;
      if (!parent) return;
      parent.children[parent.children.indexOf(this)] = next;
      next.parentNode = parent;
      this.parentNode = null;
    },
    getBoundingClientRect: () => ({ width: 800, height: 600 }),
  });

  const container = { children: [] };
  const canvas = makeCanvas();
  container.children.push(canvas);
  canvas.parentNode = container;

  let probes = 0;
  let webgpuBuilds = 0;
  let webglBuilds = 0;
  const previousWindow = globalThis.window;
  const previousLocation = globalThis.location;
  globalThis.location = { search: '' };
  globalThis.window = {
    devicePixelRatio: 1,
    BABYLON: {
      WebGPUEngine: class {
        // A getter, so every capability read is counted rather than the
        // promise being created once and reused.
        static get IsSupportedAsync() { probes += 1; return Promise.resolve(true); }
        constructor(c) { webgpuBuilds += 1; c.getContext('webgpu'); }
        async initAsync() { throw new Error('WebGPU device request failed') }
        dispose() {}
      },
      Engine: class {
        constructor(c) { webglBuilds += 1; c.getContext('webgl2'); }
        getHardwareScalingLevel() { return 1; }
        setHardwareScalingLevel() {}
        resize() {}
      },
    },
  };

  const engine = new ArenaEngine(canvas, {});
  // Two inits on ONE instance is exactly what a between-round rebuild does.
  // Scene construction past the engine is not stubbed; both engines are built
  // by then, which is the whole question.
  try { await engine.init() } catch { /* scene setup is out of scope */ }
  const afterFirst = { probes, webgpuBuilds, webglBuilds, canvases: nextId };
  try { await engine.init() } catch { /* scene setup is out of scope */ }
  globalThis.window = previousWindow;
  globalThis.location = previousLocation;

  assert.deepEqual(afterFirst, { probes: 1, webgpuBuilds: 1, webglBuilds: 1, canvases: 2 },
    'the first init must probe once, try WebGPU once, and swap the canvas once');
  assert.equal(probes, 1,
    'the second init re-probed WebGPU: that is up to WEBGPU_PROBE_TIMEOUT_MS of stall per round boundary');
  assert.equal(webgpuBuilds, 1,
    'the second init built another WebGPU engine whose device request will fail again');
  assert.equal(nextId, 2,
    'the second init swapped the canvas again, forcing a fresh WebGL context and a full shader recompile');
  assert.equal(webglBuilds, 2, 'each init must still end up with a WebGL engine');
  assert.equal(container.children.length, 1, 'the container must still hold exactly one canvas');
}

console.log('the WebGL fallback renders into a canvas it can actually present, and stops retrying WebGPU');
