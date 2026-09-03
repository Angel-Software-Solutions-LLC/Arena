import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');
const isolatedSource = source.replace(/import[\s\S]*?from '[^']+';\r?\n/g, '');
const moduleURL = `data:text/javascript;base64,${Buffer.from(isolatedSource).toString('base64')}`;
const { webGPUAvailableWithin } = await import(moduleURL);

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
